//go:build integration && !windows

package integration

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// hardKillPollTimeout bounds how long the test waits for the runner's mock
// engine to echo the sentinel line back through the pty, and separately how
// long it waits for the process table to reflect the reap. Generous for CI:
// the runner spawn is a real self-exec, "observed to take over a second under
// load" per viewer_pty_test.go's ptyRunTimeout comment.
const hardKillPollTimeout = 20 * time.Second
const hardKillPollInterval = 25 * time.Millisecond

// hardKillSentinel is the line typed into the pty whose echo proves the
// runner child is parked in the mock's echo loop. Any non-empty line works;
// this one is distinctive enough that its echo cannot be confused with
// anything else the run prints.
const hardKillSentinel = "hardkill-sentinel"

// TestRunnerReapedOnHardKilledParent proves, against the REAL product binary
// and a real `ctxloom runner mock` subprocess, that a hard-killed parent does
// not leave that subprocess behind — with NO harness intervention
// whatsoever. The parent ("ctxloom run") takes a raw, uncatchable SIGKILL and
// so gets zero chance to run its own teardown (HostRunner.Kill): the same
// shape as an agent harness tearing a test run down, `go test`'s own process
// dying, an OOM kill, or a worktree deleted out from under a live run. Only
// the kernel can reap the runner then, through the PR_SET_PDEATHSIG that
// isolation.isolateRunner arms (setRunnerPdeathsig).
//
// The harness reap (testenv.RunnerChildrenOf + testenv.KillPids, wired into
// PTYSession.Close and MCPSession.Close and exercised by this test's own
// t.Cleanup(sess.Close)) can only protect processes this harness itself
// spawned; nothing in it protects `ctxloom run` in a developer's terminal, so
// this test must prove the product-side mechanism with the harness reap held
// out of the way.
//
// This is the payload assertion the task asked for: process-table absence,
// not "the reap function returned without error".
//
// CTXLOOM_MOCK_ECHO_STDIN keeps the run alive reading lines off an open pty
// (internal/engines/mock/backend.go's executeInteractiveEcho: parks until "quit"
// or EOF) instead of the normal interactive `run`, whose mock-backed session
// round-trips and exits on its own on the order of 10ms (viewer_pty_test.go)
// — far too fast to reliably observe, let alone hard-kill, the runner
// subprocess mid-flight. A real pty (testenv.RunPTY,
// aymanbagabas/go-pty — the F2 binary-level harness viewer_pty_test.go
// established) is what makes the CLI take the interactive path at all
// (internal/adapters/cli/run_terminal.go's interactiveTerminal requires stdin to be an
// actual tty, which a plain io.Pipe is not).
//
// The echo mode is only load-bearing if the test PROVES the child is parked
// in it before capturing PIDs. The runner is spawned regardless of what the
// mock does with stdin, so a child in the process table proves nothing
// about what holds the session open: merely polling for one stays green with
// the echo path disabled outright (a coordinator-side mutation confirmed it),
// because on a quiet box the poll finds the child during startup — it
// wins a race, which on a loaded box is an intermittent shaped exactly like
// the leak it guards. So the test types a sentinel line and waits for the
// mock to echo it back through the pty: that echo can only come from a live
// runner child blocked in executeInteractiveEcho, and that loop is what
// keeps the child alive until the SIGKILL below. Disabling the echo path
// turns this test red.
func TestRunnerReapedOnHardKilledParent(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "hardkill-fragment", nil, "hard-kill reap test content")

	sess, err := env.RunPTY(80, 24, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "-f", "hardkill-fragment")
	require.NoError(t, err, "start ctxloom run")
	// Close is the harness's own safety net (SIGTERM, escalate to SIGKILL,
	// sweep any still-living runner child) for a failing run of THIS test —
	// registered via t.Cleanup so it always runs, but strictly AFTER the
	// require.Eventually below has already independently observed whether the
	// mechanism under test (PR_SET_PDEATHSIG) worked on its own.
	//
	// THE INVARIANT this registration keeps, and the one every cleanup in
	// tests/integration must keep: a cleanup that releases a resource is
	// registered at the moment the resource exists, unconditionally, with
	// nothing between the acquisition and the registration that can end the
	// test. Neither a flag consulted inside the cleanup body nor a require
	// sitting above it may decide whether the release happens. Get that
	// ordering wrong and the release is skipped on precisely the failure it
	// exists to clean up after: the only run that leaks is the run that
	// failed, which is also the run nobody is watching.
	//
	// A cleanup may branch on t.Failed() only to EMIT DIAGNOSTICS, as the
	// next one does — never to decide whether to release something.
	t.Cleanup(sess.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("pty output: %s", sess.Output())
		}
	})

	parentPID := sess.PID()

	// Type the sentinel now; the pty buffers it until the interactive path
	// reads stdin, so there is nothing to wait for first. Its echo is the
	// readiness signal: the runner subprocess ("ctxloom runner mock",
	// isolation.StartHostRunner) is up, is this process's
	// child, and is parked in the echo loop that holds the session open.
	_, err = sess.Write([]byte(hardKillSentinel + "\n"))
	require.NoError(t, err, "type sentinel into pty")
	echoed := "mock echo: " + hardKillSentinel
	require.True(t, sess.WaitForOutput(hardKillPollTimeout, func(out string) bool {
		return strings.Contains(out, echoed)
	}), "mock never echoed %q back through the pty — the runner child is not parked in the echo loop, so nothing holds it alive for the kill; output:\n%s", hardKillSentinel, sess.Output())

	childPIDs := testenv.RunnerChildrenOf(parentPID)
	require.NotEmpty(t, childPIDs, "mock echoed the sentinel but no runner subprocess is a child of pid %d", parentPID)
	childPID := childPIDs[0]
	require.True(t, processAlive(childPID), "sanity: captured runner pid %d isn't actually alive", childPID)

	// THE adversarial action: kill the parent hard. SIGKILL is uncatchable —
	// the run's signal.NotifyContext(shutdownSignals) graceful path
	// (SIGTERM/SIGINT/SIGHUP -> HostRunner.Kill) NEVER RUNS. This is
	// exactly the failure mode named in the task: kill -9, panic, a deleted
	// worktree, or an agent harness tearing the process down — the parent
	// gets no chance to reap anything itself.
	require.NoError(t, syscall.Kill(parentPID, syscall.SIGKILL))
	exited, _ := sess.Wait(hardKillPollTimeout) // reap the zombie; the (SIGKILL) exit error is expected and irrelevant
	require.True(t, exited, "parent process %d was not reaped within %s of SIGKILL", parentPID, hardKillPollTimeout)

	// NOTHING IS DONE HERE ON PURPOSE. No KillPids, no signal, no sweep —
	// the harness deliberately abandons the runner child exactly as a dying
	// agent harness or a `kill -9`'d terminal session would. Anything this
	// test did at this point would be indistinguishable from the mechanism
	// under test.
	//
	// PAYLOAD assertion: the process is actually gone from the process table.
	// Not that a cleanup function returned — a cleanup that reports success
	// while the process keeps running is precisely the defect this is the
	// regression test for, and this project's characteristic bug is exit 0
	// with nothing actually done.
	require.Eventually(t, func() bool {
		return !processAlive(childPID)
	}, hardKillPollTimeout, hardKillPollInterval,
		"runner subprocess pid %d outlived its hard-killed parent %d with nothing left to reap it — an orphaned runner", childPID, parentPID)
}

// processAlive reports whether pid names a live, non-zombie process. A bare
// kill(pid, 0) probe succeeds against a zombie too, which would make this
// exact reap test pass or fail on the environment's reaping behavior rather
// than on the product path it exists to check — see
// internal/testsupport/procalive, shared with internal/adapters/isolation's
// own reap tests so the two checks cannot drift apart.
func processAlive(pid int) bool {
	return procalive.Alive(pid)
}
