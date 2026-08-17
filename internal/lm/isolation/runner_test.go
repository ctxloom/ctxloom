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

// TestKill_ImposesOwnTimeout: the go-plugin fork calls
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

// TestKill_SurfacesLeakOnRemoveFailure: when the remove fails
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

// TestKill_AlreadyGoneIsNotALeak: a racing --rm already removed the
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
	// The SECOND benign race (ISO1, live-verified against a real docker daemon
	// via TestACPContainerTransport_RealTurn: a long-lived attached container
	// exiting right as our own `rm -f` lands hits this message 100% of the
	// time on this docker version) — docker's OWN async --rm cleanup is
	// in-flight at the exact moment ours runs; the container is gone (or
	// guaranteed to become so) either way, not a leak.
	_, inProgress := exec.Command("sh", "-c",
		"echo 'Error response from daemon: removal of container abc is already in progress' >&2; exit 1").Output()
	assert.True(t, removeReportsGone(gone))
	assert.True(t, removeReportsGone(inProgress))
	assert.False(t, removeReportsGone(wedged))
	assert.False(t, removeReportsGone(errors.New("context deadline exceeded")))
}

// TestKill_ReapedRunProcessIsRoutineTeardownNotAnError pins that
// a review row claimed Kill "unconditionally returns nil and swallows
// r.cmd.Process.Kill(), so go-plugin can never observe a failed teardown".
// Both halves of the mechanism are true and both are deliberate:
//
//   - The remove is the REAL stop and it is not silent — a remove that does
//     not confirm the container is gone streams a named, fix-it-carrying
//     warning. go-plugin, by contrast,
//     can act on nothing: its two call sites Debug-log the error (discarded at
//     our default verbosity) and discard it outright.
//   - The trailing cmd.Process.Kill only reaps our own `run` CLI, which the
//     force-remove has usually already ended and go-plugin's own Wait
//     goroutine has already reaped. Propagating that error — the row's
//     remedy — would report a failure on the ORDINARY teardown path, as this
//     test measures: an already-waited process refuses the signal.
//
// So the pin is that a fully-reaped run process is teardown SUCCESS, silently:
// no error out of Kill, no warning on stderr.
func TestKill_ReapedRunProcessIsRoutineTeardownNotAnError(t *testing.T) {
	stubKillExec(t, func(context.Context, []string) (string, error) { return "", nil })

	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	require.NoError(t, cmd.Wait()) // go-plugin's Wait goroutine gets here first

	// Measured, not assumed: this is exactly what the row's remedy would
	// propagate out of every ordinary teardown.
	require.Error(t, cmd.Process.Kill(), "a waited-for process refuses the signal — the routine post-remove state")

	done := captureStderr(t)
	r := &containerRunner{runtime: Docker{}, name: "ctxloom-iso-reaped", cmd: cmd}
	assert.NoError(t, r.Kill(context.Background()),
		"a confirmed remove plus an already-reaped run CLI is teardown success; reporting the reap as a failure would flag every ordinary shutdown")
	assert.Empty(t, done(), "no leak, so nothing to warn about")
}
