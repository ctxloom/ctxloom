package isolation

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubKillExec swaps the probeExec seam (reused by runner.Kill for the
// name-targeted remove) and restores it. Kill's cmd is an UNSTARTED `true` so
// cmd.Process is nil and the process-reap branch is a noop.
func stubKillExec(t *testing.T, fn func(ctx context.Context, args []string) (string, error)) {
	t.Helper()
	orig := probeExec
	probeExec = func(ctx context.Context, _ string, args []string) (string, error) {
		return fn(ctx, args)
	}
	t.Cleanup(func() { probeExec = orig })
}

// TestKill_ImposesOwnTimeout (finding 5): the go-plugin fork calls
// Kill(context.Background()) — no deadline — so Kill must bound the remove
// itself, or a wedged daemon hangs session teardown forever.
func TestKill_ImposesOwnTimeout(t *testing.T) {
	hasDeadline := false
	stubKillExec(t, func(ctx context.Context, _ []string) (string, error) {
		_, hasDeadline = ctx.Deadline()
		return "", nil
	})
	r := &containerRunner{runtime: Docker{}, name: "ctxloom-iso-x", cmd: exec.Command("true")}
	require.NoError(t, r.Kill(context.Background()))
	assert.True(t, hasDeadline, "Kill bounds its own remove even when handed a deadline-less ctx")
}

// TestKill_SurfacesLeakOnRemoveFailure (finding 5): when the remove fails
// (a wedged daemon, our own timeout), the container may still be alive holding
// the workspace Cleanup is about to remove — surface the leak LOUDLY with the id
// and a manual fix-it, rather than silently killing only the `run` CLI.
func TestKill_SurfacesLeakOnRemoveFailure(t *testing.T) {
	stubKillExec(t, func(context.Context, []string) (string, error) {
		return "", errors.New("context deadline exceeded")
	})
	done := captureStderr(t)
	r := &containerRunner{runtime: Docker{}, name: "ctxloom-iso-leak", cmd: exec.Command("true")}
	require.NoError(t, r.Kill(context.Background()))
	out := done()
	assert.Contains(t, out, "ctxloom-iso-leak", "the leaked container id is named")
	assert.Contains(t, out, "may still be running")
	assert.Contains(t, out, "rm -f ctxloom-iso-leak", "the fix-it names the manual removal command")
}

// TestKill_AlreadyGoneIsNotALeak (finding 5): a racing --rm already removed the
// container; `rm -f` reports "No such container" — teardown SUCCESS, no warning.
func TestKill_AlreadyGoneIsNotALeak(t *testing.T) {
	stubKillExec(t, func(context.Context, []string) (string, error) {
		_, err := exec.Command("sh", "-c", "echo 'Error: No such container: ctxloom-iso-gone' >&2; exit 1").Output()
		return "", err
	})
	done := captureStderr(t)
	r := &containerRunner{runtime: Docker{}, name: "ctxloom-iso-gone", cmd: exec.Command("true")}
	require.NoError(t, r.Kill(context.Background()))
	assert.Empty(t, done(), "an already-gone container is success, not a leak")
}

// TestKill_NoRuntimeNoop: a host-style runner (empty binary) never shells out.
func TestKill_NoRuntimeNoop(t *testing.T) {
	called := false
	stubKillExec(t, func(context.Context, []string) (string, error) { called = true; return "", nil })
	r := &containerRunner{runtime: Host{}, name: "", cmd: exec.Command("true")}
	require.NoError(t, r.Kill(context.Background()))
	assert.False(t, called, "no name/binary → no remove attempt")
}

// TestRemoveReportsGone: only the benign already-gone stderr counts as success;
// a wedged/other failure (or a non-exit error like a timeout) is a potential leak.
func TestRemoveReportsGone(t *testing.T) {
	_, gone := exec.Command("sh", "-c", "echo 'No such container: abc' >&2; exit 1").Output()
	_, wedged := exec.Command("sh", "-c", "echo 'daemon not responding' >&2; exit 1").Output()
	assert.True(t, removeReportsGone(gone))
	assert.False(t, removeReportsGone(wedged))
	assert.False(t, removeReportsGone(errors.New("context deadline exceeded")))
}
