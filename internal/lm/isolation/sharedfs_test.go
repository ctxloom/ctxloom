package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeRuntime is a fakeRuntime whose RunArgs actually renders the spec (the
// shared fake returns nil), so the probe's mount reaches the exec stub.
type probeRuntime struct{ fakeRuntime }

func (p probeRuntime) RunArgs(spec RunSpec) []string {
	return append([]string{"run", "--rm", "--name", spec.Name}, renderRunSpec(spec)...)
}

// stubProbeExec swaps the probe's exec seam for the test and restores it.
// The stub receives the marker file's host path (parsed from the rendered
// --mount bind spec) so behaviors can read or ignore the real marker. It fires
// once PER ROOT probeOneRoot execs against (calls counts every invocation).
func stubProbeExec(t *testing.T, fn func(markerPath string) (string, error)) *int {
	t.Helper()
	calls := 0
	orig := probeExec
	probeExec = func(_ context.Context, _ string, args []string) (string, error) {
		calls++
		for i, a := range args {
			if a == "--mount" && i+1 < len(args) {
				host := ""
				for _, field := range strings.Split(args[i+1], ",") {
					if src, ok := strings.CutPrefix(field, "source="); ok {
						host = src
					}
				}
				return fn(filepath.Join(host, "marker"))
			}
		}
		return fn("")
	}
	t.Cleanup(func() {
		probeExec = orig
		sharedFSMu.Lock()
		sharedFSResults = map[string]error{}
		sharedFSMu.Unlock()
	})
	return &calls
}

// TestSharedFSProbe_SharedFS: the daemon reads back the exact marker content
// → shared filesystem, nil error (plain host or true docker-in-docker).
func TestSharedFSProbe_SharedFS(t *testing.T) {
	stubProbeExec(t, func(markerPath string) (string, error) {
		b, err := os.ReadFile(markerPath)
		return string(b) + "\n", err
	})
	assert.NoError(t, sharedFSProbe(context.Background(), probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}, "img", []string{t.TempDir()}))
}

// TestSharedFSProbe_Mismatches: an empty read (the host daemon auto-created
// an empty dir on ITS filesystem — the docker-outside-of-docker signature),
// wrong content, or a failed run all report a mismatch.
func TestSharedFSProbe_Mismatches(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) (string, error)
		want string
	}{
		{"empty read (DooD dir auto-create)", func(string) (string, error) { return "", nil }, "mismatch"},
		{"wrong content", func(string) (string, error) { return "something-else", nil }, "mismatch"},
		{"run failed", func(string) (string, error) { return "", errors.New("no such file") }, "did not run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubProbeExec(t, tt.fn)
			err := sharedFSProbe(context.Background(), probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}, "img", []string{t.TempDir()})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestSharedFSProbe_Memoizes: one probe per (runtime, image, root set) per
// process — a fan of many members against the SAME roots pays for a single
// scratch container.
func TestSharedFSProbe_Memoizes(t *testing.T) {
	calls := stubProbeExec(t, func(markerPath string) (string, error) {
		b, err := os.ReadFile(markerPath)
		return string(b), err
	})
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	roots := []string{t.TempDir()}
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img", roots))
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img", roots))
	assert.Equal(t, 1, *calls, "second probe for the same runtime+image+roots is memoized")

	require.NoError(t, sharedFSProbe(context.Background(), rt, "other-img", roots))
	assert.Equal(t, 2, *calls, "a different image probes fresh")

	require.NoError(t, sharedFSProbe(context.Background(), rt, "img", []string{t.TempDir()}))
	assert.Equal(t, 3, *calls, "a different root set probes fresh even against the same runtime+image — a Docker Desktop file-sharing list grants sharing per host path, not per image")
}

// TestSharedFSProbe_TransientNotMemoized: a transient run failure
// (a cold Docker Desktop VM's first probe, a daemon stall, our timeout, a
// cancelled ctx) must NOT be latched — a later call re-probes and can succeed,
// so one cold start never pins a long-lived mcp/acp process to degrade forever.
func TestSharedFSProbe_TransientNotMemoized(t *testing.T) {
	fail := true
	calls := stubProbeExec(t, func(markerPath string) (string, error) {
		if fail {
			return "", errors.New("daemon not ready yet") // transient, non-definitive
		}
		b, err := os.ReadFile(markerPath)
		return string(b), err
	})
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	roots := []string{t.TempDir()}

	require.Error(t, sharedFSProbe(context.Background(), rt, "img", roots))
	require.Equal(t, 1, *calls)

	// Daemon warms up — the next call must RE-PROBE (not return the cached error).
	fail = false
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img", roots))
	assert.Equal(t, 2, *calls, "a transient failure is re-probed, never latched")
}

// TestSharedFSProbe_DefinitiveOutcomesMemoized is the complement: success
// AND a genuine content mismatch are permanent verdicts — both latch, so a
// fan-out pays for one probe container per (runtime, image).
func TestSharedFSProbe_DefinitiveOutcomesMemoized(t *testing.T) {
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	t.Run("content mismatch latches", func(t *testing.T) {
		roots := []string{t.TempDir()}
		calls := stubProbeExec(t, func(string) (string, error) { return "", nil }) // empty read = mismatch
		require.Error(t, sharedFSProbe(context.Background(), rt, "img", roots))
		require.Error(t, sharedFSProbe(context.Background(), rt, "img", roots))
		assert.Equal(t, 1, *calls, "a definitive content mismatch is cached like a success")
	})
}

// TestSharedFSProbe_RunFailureSurfacesStderr: a run failure is
// reported as its REAL cause — docker's stderr — not as a phantom fs-sharing
// mismatch, and it is not a definitive (memoizable) verdict.
func TestSharedFSProbe_RunFailureSurfacesStderr(t *testing.T) {
	stubProbeExec(t, func(string) (string, error) {
		// A real *exec.ExitError with populated .Stderr (what .Output() yields).
		_, err := exec.Command("sh", "-c", "echo 'Cannot connect to the daemon socket' >&2; exit 1").Output()
		return "", err
	})
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	err := sharedFSProbe(context.Background(), rt, "img", []string{t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not run")
	assert.Contains(t, err.Error(), "Cannot connect to the daemon socket", "the daemon's stderr is the real cause")

	var mism *sharedFSMismatch
	assert.False(t, errors.As(err, &mism), "a run failure is not a definitive sharing verdict")
}

// TestPrepareWorkspace_DegradesOnFSMismatch: a probe mismatch fails the
// PrepareWorkspace gate with actionable guidance — the caller's per-axis
// degrade then turns what used to be a plugin-handshake hang into a clean
// fallback. The probe now runs AFTER auth resolves and the base (project dir)
// is prepared (the reorder this fix makes), so both are stubbed/real here
// precisely to reach it deterministically regardless of host state.
func TestPrepareWorkspace_DegradesOnFSMismatch(t *testing.T) {
	origCheck := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error {
		return &sharedFSMismatch{msg: "marker content mismatch"}
	}
	t.Cleanup(func() { sharedFSCheck = origCheck })

	// A runtime whose binary is a real command (`true`) so ensureImage's
	// image-inspect exec succeeds and the gate reaches the probe.
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "mock").WithImage("img")
	// Auth must resolve for the gate to reach prepareBase/the probe at all
	// (host state — real ANTHROPIC_* creds — must never gate a hermetic test).
	c.engineSpec.resolveAuth = func(string, string) (containerAuth, bool) {
		return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
	}
	_, err := c.PrepareWorkspace(context.Background(), t.TempDir(), "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not share this process's filesystem")
	assert.Contains(t, err.Error(), "bind mounts")
}

// TestPrepareWorkspace_FSProbeRunFailureIsNotMisreportedAsMismatch: the MINOR
// headline fix — a TRANSIENT probe-run failure (the probe never produced a
// sharing verdict at all) must not be reported with the definitive "does not
// share this process's filesystem" wording, mirroring diagnoseProbe's own
// errors.As distinction (diagnose.go).
func TestPrepareWorkspace_FSProbeRunFailureIsNotMisreportedAsMismatch(t *testing.T) {
	origCheck := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error {
		return fmt.Errorf("shared-fs probe container did not run: %w", errors.New("daemon not ready"))
	}
	t.Cleanup(func() { sharedFSCheck = origCheck })

	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "mock").WithImage("img")
	c.engineSpec.resolveAuth = func(string, string) (containerAuth, bool) {
		return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
	}
	_, err := c.PrepareWorkspace(context.Background(), t.TempDir(), "m")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "does not share this process's filesystem", "a transient run failure is not a sharing verdict")
	assert.Contains(t, err.Error(), "could not run")
	assert.Contains(t, err.Error(), "daemon not ready")
}

// TestSharedFSProbe_MultiRoot is the fix's core correctness proof: probing the
// REAL mount roots (not a single synthetic tempdir) must pass when EVERY real
// root is shared, and must FAIL — no false positive — when even ONE real
// mount root among several is not shared, exactly the partially-shared Docker
// Desktop custom file-sharing list scenario this fix exists for.
func TestSharedFSProbe_MultiRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	rootC := t.TempDir()
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}

	t.Run("every real root shared: passes", func(t *testing.T) {
		stubProbeExec(t, func(markerPath string) (string, error) {
			b, err := os.ReadFile(markerPath)
			return string(b), err
		})
		err := sharedFSProbe(context.Background(), rt, "multiroot-ok", []string{rootA, rootB, rootC})
		assert.NoError(t, err)
	})

	t.Run("one root among several NOT shared: fails", func(t *testing.T) {
		var calls int
		orig := probeExec
		probeExec = func(_ context.Context, _ string, args []string) (string, error) {
			calls++
			host := ""
			for i, a := range args {
				if a == "--mount" && i+1 < len(args) {
					for _, field := range strings.Split(args[i+1], ",") {
						if src, ok := strings.CutPrefix(field, "source="); ok {
							host = src
						}
					}
				}
			}
			if strings.HasPrefix(host, rootB) {
				// rootB's daemon does not share this path: the docker-outside-of-
				// docker auto-create signature (an empty read).
				return "", nil
			}
			b, err := os.ReadFile(filepath.Join(host, "marker"))
			return string(b), err
		}
		t.Cleanup(func() {
			probeExec = orig
			sharedFSMu.Lock()
			sharedFSResults = map[string]error{}
			sharedFSMu.Unlock()
		})

		err := sharedFSProbe(context.Background(), rt, "multiroot-mismatch", []string{rootA, rootB, rootC})
		require.Error(t, err, "one unshared root among several real mounts must fail the whole gate")
		assert.Contains(t, err.Error(), rootB, "the error names the actual unshared root")
		var mism *sharedFSMismatch
		assert.True(t, errors.As(err, &mism), "a content mismatch on any root is a definitive verdict")
		assert.Equal(t, 2, calls, "fails fast: rootA passes, rootB fails, rootC (still untested) is never probed")
	})
}

// TestMountProbeRoots: the real mount-root derivation the fix threads into
// the probe — the run's cwd and the scratch root always appear; a mount whose
// Host is a directory (a config overlay, a gitdir mirror) probes ITSELF, a
// mount whose Host is a FILE (a direct read-only credential mount, e.g.
// codex's ~/.codex/auth.json) probes its PARENT dir instead — never the file
// itself, so the probe's own marker write never touches a real credential.
// Deduplicated and sorted.
func TestMountProbeRoots(t *testing.T) {
	dir := t.TempDir()
	scratch := t.TempDir()
	overlayDir := t.TempDir() // a directory mount (e.g. gitdir mirror)

	authParent := t.TempDir()
	authFile := filepath.Join(authParent, "auth.json")
	require.NoError(t, os.WriteFile(authFile, []byte("secret"), 0o600))

	got := mountProbeRoots(dir, scratch, []Mount{
		{Host: overlayDir, Container: "/x/overlay"},
		{Host: authFile, Container: "/x/auth.json"},
		{Host: scratch, Container: "/x/dup"}, // duplicate of scratch itself
	})

	want := []string{authParent, dir, overlayDir, scratch}
	sort.Strings(want)
	assert.Equal(t, want, got)

	before, err := os.ReadFile(authFile)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(before), "deriving the probe root never touches the real credential file's content")
}

// TestProbeOneRoot_SweepsAbandonedProbeDirs pins that the probe's scratch
// dir is created INSIDE the live mount root — the project directory itself —
// deliberately: a Docker Desktop custom file-sharing list grants sharing per
// host PATH, so probing anywhere else proves nothing about the path a run
// actually mounts (mountProbeRoots' doc). Normal teardown removes it, but the
// deferred RemoveAll cannot run if the process is killed, so a hard kill left
// `ctxloom-fsprobe-*` debris in the user's project dir — where it shows up in
// git status and, in this project, meets a dirty-tree gate.
//
// The probe therefore sweeps its own abandoned dirs before creating a new one.
// The sweep is age-bounded because concurrent ctxloom processes probe the same
// roots: only debris far older than a probe could possibly still be running is
// touched, so a live sibling's scratch dir is never destroyed.
func TestProbeOneRoot_SweepsAbandonedProbeDirs(t *testing.T) {
	root := t.TempDir()

	abandoned := filepath.Join(root, "ctxloom-fsprobe-1234567")
	require.NoError(t, os.MkdirAll(abandoned, 0o755))
	old := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	// A sibling process's probe, in flight right now: must survive.
	live := filepath.Join(root, "ctxloom-fsprobe-7654321")
	require.NoError(t, os.MkdirAll(live, 0o755))

	// Unrelated user content that merely sorts nearby: must never be touched.
	keep := filepath.Join(root, "ctxloom-fsprobe.md")
	require.NoError(t, os.WriteFile(keep, []byte("notes"), 0o644))
	require.NoError(t, os.Chtimes(keep, old, old))

	stubProbeExec(t, func(markerPath string) (string, error) {
		raw, err := os.ReadFile(markerPath)
		return string(raw), err
	})
	require.NoError(t, probeOneRoot(context.Background(), probeRuntime{}, "img", root))

	assert.NoDirExists(t, abandoned, "a probe dir far older than any live probe is debris and must be swept")
	assert.DirExists(t, live, "a concurrent process's in-flight probe dir must never be destroyed")
	assert.FileExists(t, keep, "only directories with the probe's own prefix are swept")

	// And the probe's own dir is gone on the ordinary path, as before.
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	var probeDirs int
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "ctxloom-fsprobe-") {
			probeDirs++
		}
	}
	assert.Equal(t, 1, probeDirs, "only the sibling's live dir remains; this probe removed its own")
}
