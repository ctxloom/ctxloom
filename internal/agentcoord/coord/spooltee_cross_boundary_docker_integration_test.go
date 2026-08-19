//go:build docker_integration

// The docker-gated proof that the shadow tee's DOORBELL PATH crosses a real
// container boundary: a file the coordinator wrote on the host is located, in
// a second filesystem view, from NOTHING BUT the reference the doorbell
// carried over the wire.
//
// Why this test and not "eventual delivery". The design's own honest counter
// names the failure mode this guards (R6.3): the file layout and the doorbell
// ref are two things that must agree, and once the sweep exists, a skew
// between them fails as LATENCY — the sweep still delivers, just slower —
// which is this project's characteristic bug wearing a new coat. So the
// assertion here deliberately uses the doorbell ref ALONE and never lists a
// directory: a wrong ref is ENOENT and the test is red, not slow.
//
// Run with:
//
//	just test-pkg ./internal/agentcoord/coord -tags docker_integration -run SpoolTeeCrossBoundary
package coord

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

const (
	crossBoundaryImage = "alpine:latest"
	// crossBoundaryContainerHome is deliberately NOT the host fixture path.
	// If the two views were the same string this test would pass with a
	// mapper that baked in a sender-view absolute path, which is the exact
	// defect the Ref/PathMapper seam exists to prevent.
	crossBoundaryContainerHome = "/chome"
)

// TestSpoolTeeCrossBoundary_DoorbellRefResolvesInTheContainerView drives the
// real stack — a tee-enabled coordinator, a migrated child, a live runner Home
// over the coordinator's own listeners — and then follows the doorbell across a
// real bind mount.
//
// WHAT IT PROVES:
//
//   - The doorbell rides the real wire and the real proto conversion. The ref
//     the runner acts on came through CoordinatorNotice.SpoolChanged,
//     SpoolChangedProto/SpoolRefFromProto and the S2 receive chokepoint —
//     not from a Go value the test handed across.
//   - That ref is VIEW-INDEPENDENT. The identical HomeMapper resolves it to
//     two different absolute paths under two different homes, and the
//     container path — reached only by resolving the ref, never by listing a
//     directory — names the exact bytes the coordinator's tee wrote.
//   - Rename-publish holds across the mount: the container reads a complete,
//     parseable message, not a torn one.
//
// WHAT IT DOES NOT PROVE:
//
//   - It does not run a coordinator or a runner inside a container. Both live
//     in this process; only the READ of the teed file happens in the
//     container. A defect in how a containerized runner dials home, or in the
//     env stamp a real container spawn carries, is out of its reach — the
//     ordinary docker-gated run tests cover that ground.
//   - The container-side resolution is performed by this process with $HOME
//     swapped, not by a ctxloom process running inside the container. It is
//     the same code path resolving the same ref under the other view (the
//     approximation S1's cross-mount test makes with its two views), so it
//     proves the mapper's view-independence; it does not prove that a
//     container's OWN ctxloom picks up the right $HOME.
//   - It says nothing about the sweep, which does not exist yet. That is the
//     point: the only delivery mechanism under test is the doorbell.
func TestSpoolTeeCrossBoundary_DoorbellRefResolvesInTheContainerView(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the spool tee cross-boundary integration test")
	resetStrictness(t)

	// A real filesystem outside the checkout, for the same two reasons the
	// spool package's own fixture uses one: /tmp is tmpfs on a stock Linux box
	// (a durable substrate proven only over RAM is evidence about the wrong
	// thing), and in-tree residue confuses worktree-safe WIP detection even
	// when .gitignore hides it.
	fixture, err := os.MkdirTemp(crossBoundaryFixtureRoot(t), "ctxloom-spool-tee-xb-")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Loud on purpose: leftover fixture dirs are machine debris a later
		// run reads as signal.
		if err := os.RemoveAll(fixture); err != nil {
			t.Errorf("removing the cross-boundary fixture %s: %v", fixture, err)
		}
	})
	t.Setenv("HOME", fixture)

	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", viaStartRun: true},
	}, nil)
	sp.engineCaps = RunnerCapabilities(true)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	home := awaitRunnerHome(t, c, sp, out.Harp)

	// The RUNNER's handler is the receiving end: whatever lands here has
	// already passed the wire, the proto conversion and the validation
	// chokepoint.
	rings := make(chan spool.Ref, 4)
	home.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { rings <- ref })

	const body = "cross-boundary tee body\n"
	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, body, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	var ref spool.Ref
	select {
	case ref = <-rings:
	case <-time.After(30 * time.Second):
		t.Fatal("the coordinator teed a file but its doorbell never reached the runner")
	}
	require.NoError(t, ref.Validate(), "a doorbell that reached a handler must carry a usable ref")
	assert.Equal(t, out.Harp, ref.Harp)
	assert.Equal(t, spool.DirIn, ref.Dir)

	hostPath, err := spool.NewHomeMapper().Resolve(ref)
	require.NoError(t, err)
	hostBytes, err := os.ReadFile(hostPath)
	require.NoError(t, err)
	require.NotEmpty(t, hostBytes, "empty-source guard: a zero-byte file would satisfy a naive 'the bytes match' check")

	// Quiesce BEFORE swapping $HOME: a live coordinator resolves spool paths
	// on every tee write, and a global swap under it would be a race that
	// shows up as an occasional red in someone else's branch. Close is
	// idempotent, so the constructor's own cleanup still runs.
	c.Close()

	// The container view is resolved by THIS process with $HOME pointing at the
	// container's home — the second-view approximation this file's doc names.
	// The swap is global to the process and lasts to the end of the test:
	// nothing below reads the process $HOME (containerRead hands docker the
	// container's home explicitly), and the coordinator that did was quiesced
	// above.
	t.Setenv("HOME", crossBoundaryContainerHome)
	containerPath, err := spool.NewHomeMapper().Resolve(ref)
	require.NoError(t, err)
	require.NotEqual(t, hostPath, containerPath,
		"the two views must DIFFER, or this test proves nothing about view-independence")
	require.True(t, strings.HasPrefix(containerPath, crossBoundaryContainerHome+"/"),
		"the container view must resolve under the container's own home, got %q", containerPath)
	require.Equal(t,
		strings.TrimPrefix(hostPath, fixture),
		strings.TrimPrefix(containerPath, crossBoundaryContainerHome),
		"the two views must share the identical home-relative tail")

	// The container is handed ONLY the doorbell-derived path. No readdir, no
	// sweep, no glob: a ref or layout skew is an ENOENT and a red test, never
	// a slower delivery.
	got := containerRead(t, fixture, containerPath)
	require.Equal(t, string(hostBytes), got,
		"the container must read exactly the bytes the tee wrote, byte for byte")

	msg, err := spool.Parse([]byte(got))
	require.NoError(t, err, "the container must read a COMPLETE message, not a torn one")
	assert.Equal(t, KindMessage, msg.Kind)
	assert.Equal(t, body, msg.Body)
	assert.Equal(t, msgID, msg.OriginID, "the file the doorbell named must be the twin of the mailbox delivery that caused it")
}

// containerRead bind-mounts the fixture home at the container home and reads
// one absolute container-view path. Stock alpine, one `docker run`, no image
// build and no probe binary: everything under test already happened on the
// host, and all the container supplies is the second filesystem view.
func containerRead(t *testing.T, fixture, containerPath string) string {
	t.Helper()
	args := []string{"run", "--rm"}
	if !dockergate.DockerIsRootless() {
		// Rootful daemon: without this the container reads as real root,
		// which a 0700 spool owned by the test user refuses. Rootless already
		// maps container root onto the invoking user.
		args = append(args, "--user", strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()))
	}
	args = append(args,
		"-v", fixture+":"+crossBoundaryContainerHome,
		"-e", "HOME="+crossBoundaryContainerHome,
		crossBoundaryImage,
		"cat", containerPath,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "the container could not read the doorbelled path %s:\n%s", containerPath, out)
	require.NotEmpty(t, out, "the container read zero bytes from %s", containerPath)
	return string(out)
}

// crossBoundaryFixtureRoot returns the parent directory for the fixture home:
// outside the source tree and on a real (non-tmpfs) filesystem. /var/tmp
// satisfies both by convention; the env override exists for a machine where it
// is unusable, and the fallback is the checkout's parent rather than /tmp so
// the "real filesystem" property survives.
func crossBoundaryFixtureRoot(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("CTXLOOM_TEST_FIXTURE_ROOT"); override != "" {
		return override
	}
	const varTmp = "/var/tmp"
	if info, err := os.Stat(varTmp); err == nil && info.IsDir() {
		return varTmp
	}
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must locate this test file")
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	require.NoError(t, err)
	return root
}
