//go:build docker_integration

// The docker-gated proof that this library's operations hold ACROSS A REAL
// BIND MOUNT — the one claim the unit tests cannot make, because both sides
// of a container boundary are the same process there.
//
// What it proves, in one container run:
//
//   - Cross-view resolution is real, not a coincidence of one home. The
//     container resolves the spool under ITS home (/chome/.ctxloom/...) while
//     the host resolves it under the fixture home; the two absolute paths
//     DIFFER and name the same bytes. A mapper that had baked in a
//     sender-view path would fail here and nowhere else.
//   - Host consume-rename is visible in-container: a message the host wrote
//     into in/ and then consumed is GONE from in/ and PRESENT in in/consumed/
//     when the container sweeps. That is the whole cursor model crossing the
//     mount.
//   - Container write-fsync-rename is visible on the host BYTE-COMPLETE: the
//     host sweeps out/ and parses a message with the exact body the container
//     wrote. Rename-publish is what makes that safe (never tail or read a
//     growing file across a mount — the published-evidence survey's DD-Mac
//     truncated-read bug is exactly that shape).
//
// Shape and cost: no image build. A stock alpine plus the package's OWN test
// binary, statically linked and bind-mounted in, driven through TestMain's
// probe mode. One `docker run`, ~1s of container time, no ctxloom image, no
// daemon setup beyond reachability.
//
// Run with:
//
//	just test-docker-integration
//	just test-pkg ./internal/agentcoord/spool -tags docker_integration -run SpoolCrossMount
package spool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// Probe-mode environment. The container side is this same test binary
// re-entered through TestMain, which is why there is no separate command to
// build, keep in sync, or forget to rebuild: the container exercises the
// EXACT library code this test run compiled.
const (
	envProbe       = "CTXLOOM_SPOOL_PROBE"
	envProbeHarp   = "CTXLOOM_SPOOL_PROBE_HARP"
	envProbeMarker = "CTXLOOM_SPOOL_PROBE_MARKER"
)

// TestMain re-enters as the container-side probe when envProbe is set, and is
// an ordinary test main otherwise.
//
// A probe rather than a t.Skip-guarded test on purpose: a bare t.Skip in a
// docker-gated file is invisible reachability policy (build/gates.justfile
// fails the build on one), and this entry point never wants to report
// "skipped" — on the host it simply is not the entry point.
func TestMain(m *testing.M) {
	if phase := os.Getenv(envProbe); phase != "" {
		os.Exit(runProbe(phase))
	}
	os.Exit(m.Run())
}

// runProbe is the container side. It prints machine-readable lines the host
// asserts on and returns a non-zero status with a reason on any failure —
// a probe that exited 0 having checked nothing is the failure mode this whole
// suite exists to catch.
func runProbe(phase string) int {
	harp := os.Getenv(envProbeHarp)
	marker := os.Getenv(envProbeMarker)
	if phase != "roundtrip" {
		fmt.Fprintf(os.Stderr, "probe: unknown phase %q\n", phase)
		return 2
	}
	if harp == "" || marker == "" {
		fmt.Fprintf(os.Stderr, "probe: %s and %s are required\n", envProbeHarp, envProbeMarker)
		return 2
	}
	m := NewHomeMapper()

	// 1. The host wrote one in/ message and consumed it. In-container, in/
	//    must be EMPTY and in/consumed/ must hold it.
	live, err := Sweep(m, harp, DirIn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: sweeping in/: %v\n", err)
		return 1
	}
	if err := live.ProblemErr(); err != nil {
		fmt.Fprintf(os.Stderr, "probe: in/ has unreadable files: %v\n", err)
		return 1
	}
	if len(live.Entries) != 0 {
		fmt.Fprintf(os.Stderr, "probe: in/ should be empty after the host consumed, found %d\n", len(live.Entries))
		return 1
	}
	consumed, err := Sweep(m, harp, DirInConsumed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: sweeping in/consumed: %v\n", err)
		return 1
	}
	if err := consumed.ProblemErr(); err != nil {
		fmt.Fprintf(os.Stderr, "probe: in/consumed has unreadable files: %v\n", err)
		return 1
	}
	if len(consumed.Entries) != 1 {
		fmt.Fprintf(os.Stderr, "probe: in/consumed should hold exactly the consumed message, found %d\n", len(consumed.Entries))
		return 1
	}
	wantBody := marker + "-in\n"
	if got := consumed.Entries[0].Message.Body; got != wantBody {
		fmt.Fprintf(os.Stderr, "probe: consumed body %q, want %q\n", got, wantBody)
		return 1
	}
	consumedPath, err := m.Resolve(consumed.Entries[0].Ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: resolving the consumed ref: %v\n", err)
		return 1
	}
	fmt.Printf("PROBE_CONSUMED_PATH=%s\n", consumedPath)

	// 2. Write one out/ message for the host to read back.
	w, err := NewWriter(m, harp, DirOut, "agentprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: creating the out writer: %v\n", err)
		return 1
	}
	ref, err := w.Write(&Message{Kind: "report", FromHarp: harp, To: "parent", Body: marker + "-out\n"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: writing out/: %v\n", err)
		return 1
	}
	outPath, err := m.Resolve(ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: resolving the written ref: %v\n", err)
		return 1
	}
	fmt.Printf("PROBE_OUT_NAME=%s\n", ref.Name)
	fmt.Printf("PROBE_OUT_PATH=%s\n", outPath)
	fmt.Println("PROBE_OK")
	return 0
}

const (
	crossMountImage    = "alpine:latest"
	containerHome      = "/chome"
	containerProbeDir  = "/probe"
	containerProbeName = "spool.test"
)

func TestSpoolCrossMount_HostAndContainerShareOneSpool(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the spool cross-mount integration test")

	const harp = "ugly-icy-squid"
	marker := fmt.Sprintf("xmount-%d", time.Now().UnixNano())

	// Build the probe BEFORE $HOME is redirected. `go test -c` derives GOPATH
	// from $HOME, so building under the fixture home rebuilds the whole module
	// cache inside the fixture — read-only trees the cleanup then cannot
	// remove, leaving debris in the repo and adding ~10s to the run. Measured,
	// not theorised: this test left two undeletable .spool-xmount-* dirs
	// behind before the reorder.
	probeDir := t.TempDir()
	buildProbe(t, filepath.Join(probeDir, containerProbeName))

	// The fixture home lives on the REPO's filesystem, not TMPDIR: /tmp is
	// tmpfs on a stock Linux box, and a spool substrate proven only over a
	// RAM filesystem would be evidence about the wrong thing.
	fixture, err := os.MkdirTemp(repoRoot(t), ".spool-xmount-")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Loud on purpose: leftover fixture dirs are machine debris that a
		// later run reads as signal, and a swallowed error here is how they
		// accumulate unnoticed.
		if err := os.RemoveAll(fixture); err != nil {
			t.Errorf("removing the cross-mount fixture %s: %v", fixture, err)
		}
	})
	t.Setenv("HOME", fixture)

	m := NewHomeMapper()
	require.NoError(t, EnsureDirs(m, harp))

	// Host side: write one in/ message and consume it, so the container has
	// both a rename to observe and an empty in/ to confirm.
	w, err := NewWriter(m, harp, DirIn, "coord")
	require.NoError(t, err)
	inRef, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: harp, Body: marker + "-in\n"})
	require.NoError(t, err)
	consumedRef, err := Consume(m, inRef)
	require.NoError(t, err)
	hostConsumedPath, err := m.Resolve(consumedRef)
	require.NoError(t, err)

	args := []string{"run", "--rm"}
	if !dockerIsRootless(t) {
		// Rootful daemon: without this the container writes as real root and
		// the host cannot read back (or clean up) what it wrote. Rootless
		// already maps container root to the invoking user.
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	args = append(args,
		"-v", fixture+":"+containerHome,
		"-v", probeDir+":"+containerProbeDir+":ro",
		"-e", "HOME="+containerHome,
		"-e", envProbe+"=roundtrip",
		"-e", envProbeHarp+"="+harp,
		"-e", envProbeMarker+"="+marker,
		"-w", containerHome,
		crossMountImage,
		containerProbeDir+"/"+containerProbeName,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "container probe failed:\n%s", out)
	require.Contains(t, string(out), "PROBE_OK", "container probe did not reach its end:\n%s", out)

	// The container saw the host's consume-rename — and saw it at a DIFFERENT
	// absolute path than the host uses, which is the cross-view property the
	// whole PathMapper seam exists for.
	containerConsumedPath := probeValue(t, string(out), "PROBE_CONSUMED_PATH")
	require.True(t, strings.HasPrefix(containerConsumedPath, containerHome+"/"),
		"container must resolve under its own home, got %q", containerConsumedPath)
	require.NotEqual(t, hostConsumedPath, containerConsumedPath,
		"host and container views must differ, or this test proves nothing")
	require.Equal(t,
		strings.TrimPrefix(hostConsumedPath, fixture),
		strings.TrimPrefix(containerConsumedPath, containerHome),
		"the two views must share the identical home-relative tail")

	// The container's write is visible on the host, byte-complete and
	// parseable — the rename-publish contract across the mount.
	outName := probeValue(t, string(out), "PROBE_OUT_NAME")
	res, err := Sweep(m, harp, DirOut)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr(), "the container's message must not land malformed on the host")
	require.Len(t, res.Entries, 1)
	require.Equal(t, outName, res.Entries[0].Ref.Name)
	require.Equal(t, marker+"-out\n", res.Entries[0].Message.Body,
		"the host must read exactly the body the container wrote")
	require.Equal(t, "report", res.Entries[0].Message.Kind)
	require.Equal(t, harp, res.Entries[0].Message.FromHarp)

	hostOutPath, err := m.Resolve(res.Entries[0].Ref)
	require.NoError(t, err)
	raw, err := os.ReadFile(hostOutPath)
	require.NoError(t, err)
	require.NotEmpty(t, raw, "empty-source guard: a zero-byte file would satisfy a naive 'it exists' check")
	require.NotEqual(t, probeValue(t, string(out), "PROBE_OUT_PATH"), hostOutPath)

	// And the host can consume what the container wrote: the reverse
	// direction's rename works over the same mount.
	outConsumed, err := Consume(m, res.Entries[0].Ref)
	require.NoError(t, err)
	after, err := os.ReadFile(mustResolve(t, m, outConsumed))
	require.NoError(t, err)
	require.Equal(t, raw, after, "consume must move the container's bytes, not rewrite or drop them")
}

// buildProbe compiles this package's test binary as a static linux executable
// for the host architecture (a hardcoded amd64 would `exec format error` on an
// arm64 host, since alpine:latest resolves to the host's arch).
func buildProbe(t *testing.T, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-tags", "docker_integration",
		"-o", out, "github.com/ctxloom/ctxloom/internal/agentcoord/spool")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the container-side probe: %v\n%s", err, combined)
	}
}

// repoRoot locates the module root from this file's own path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must locate this test file")
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "go.mod"), "derived repo root must contain go.mod")
	return root
}

// dockerIsRootless reports whether the daemon is rootless, which decides
// whether the run needs an explicit --user.
func dockerIsRootless(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "rootless")
}

// probeValue extracts one KEY=value line the probe printed.
func probeValue(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, key+"="); ok {
			require.NotEmpty(t, rest, "probe printed an empty %s", key)
			return rest
		}
	}
	t.Fatalf("probe output has no %s line:\n%s", key, out)
	return ""
}

func mustResolve(t *testing.T, m PathMapper, ref Ref) string {
	t.Helper()
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	return path
}
