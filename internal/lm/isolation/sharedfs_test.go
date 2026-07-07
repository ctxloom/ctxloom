package isolation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
// --mount bind spec) so behaviors can read or ignore the real marker.
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
	assert.NoError(t, sharedFSProbe(context.Background(), probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}, "img"))
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
			err := sharedFSProbe(context.Background(), probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}, "img")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestSharedFSProbe_Memoizes: one probe per (runtime, image) per process — a
// fan of many members pays for a single scratch container.
func TestSharedFSProbe_Memoizes(t *testing.T) {
	calls := stubProbeExec(t, func(markerPath string) (string, error) {
		b, err := os.ReadFile(markerPath)
		return string(b), err
	})
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img"))
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img"))
	assert.Equal(t, 1, *calls, "second probe for the same runtime+image is memoized")

	require.NoError(t, sharedFSProbe(context.Background(), rt, "other-img"))
	assert.Equal(t, 2, *calls, "a different image probes fresh")
}

// TestSharedFSProbe_TransientNotMemoized (finding 1): a transient run failure
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

	require.Error(t, sharedFSProbe(context.Background(), rt, "img"))
	require.Equal(t, 1, *calls)

	// Daemon warms up — the next call must RE-PROBE (not return the cached error).
	fail = false
	require.NoError(t, sharedFSProbe(context.Background(), rt, "img"))
	assert.Equal(t, 2, *calls, "a transient failure is re-probed, never latched")
}

// TestSharedFSProbe_DefinitiveOutcomesMemoized (finding 1 complement): success
// AND a genuine content mismatch are permanent verdicts — both latch, so a
// fan-out pays for one probe container per (runtime, image).
func TestSharedFSProbe_DefinitiveOutcomesMemoized(t *testing.T) {
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	t.Run("content mismatch latches", func(t *testing.T) {
		calls := stubProbeExec(t, func(string) (string, error) { return "", nil }) // empty read = mismatch
		require.Error(t, sharedFSProbe(context.Background(), rt, "img"))
		require.Error(t, sharedFSProbe(context.Background(), rt, "img"))
		assert.Equal(t, 1, *calls, "a definitive content mismatch is cached like a success")
	})
}

// TestSharedFSProbe_RunFailureSurfacesStderr (finding 3): a run failure is
// reported as its REAL cause — docker's stderr — not as a phantom fs-sharing
// mismatch, and it is not a definitive (memoizable) verdict.
func TestSharedFSProbe_RunFailureSurfacesStderr(t *testing.T) {
	stubProbeExec(t, func(string) (string, error) {
		// A real *exec.ExitError with populated .Stderr (what .Output() yields).
		_, err := exec.Command("sh", "-c", "echo 'Cannot connect to the daemon socket' >&2; exit 1").Output()
		return "", err
	})
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}
	err := sharedFSProbe(context.Background(), rt, "img")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not run")
	assert.Contains(t, err.Error(), "Cannot connect to the daemon socket", "the daemon's stderr is the real cause")

	var mism *sharedFSMismatch
	assert.False(t, errors.As(err, &mism), "a run failure is not a definitive sharing verdict")
}

// TestPrepareContainerScratch_DegradesOnFSMismatch: a probe mismatch fails
// the shared gate with actionable guidance — the caller's per-axis degrade
// then turns what used to be a plugin-handshake hang into a clean fallback.
func TestPrepareContainerScratch_DegradesOnFSMismatch(t *testing.T) {
	origCheck := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string) error {
		return errors.New("marker content mismatch")
	}
	t.Cleanup(func() { sharedFSCheck = origCheck })

	// A runtime whose binary is a real command (`true`) so ensureImage's
	// image-inspect exec succeeds and the gate reaches the probe.
	c := NewContainer(fakeRuntime{name: "docker", binary: "true", available: true}, "img")
	_, err := c.PrepareWorkspace(context.Background(), "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not share this process's filesystem")
	assert.Contains(t, err.Error(), "bind mounts")
}
