//go:build integration && !windows

package integration

import (
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// hardKillPollTimeout bounds how long the test waits for the mock plugin
// subprocess to appear as a child of the ctxloom-under-test process, and
// separately how long it waits for the process table to reflect the reap.
// Generous for CI: the plugin spawn is a real self-exec + go-plugin
// handshake, "observed to take over a second under load" per
// viewer_pty_test.go's ptyRunTimeout comment.
const hardKillPollTimeout = 20 * time.Second
const hardKillPollInterval = 25 * time.Millisecond

// TestMockPluginReapedOnHardKilledParent proves, against the REAL product
// binary and a real `ctxloom llm serve mock` subprocess, that a hard-killed
// parent no longer leaves that subprocess behind — with NO harness
// intervention whatsoever. The parent ("ctxloom run") takes a raw,
// uncatchable SIGKILL and so gets zero chance to run its own `defer
// client.Kill()` cleanup: the same shape as an agent harness tearing a test
// run down, `go test`'s own process dying, an OOM kill, or a worktree deleted
// out from under a live run. Every one of those produced orphans until
// isolateRunner started arming PR_SET_PDEATHSIG (internal/lm/grpc/
// pdeath_linux.go) — real orphaned processes across multiple checkouts, some
// running unattended for over 36 hours.
//
// This test USED to assert the opposite as its premise ("the plugin child is
// now an orphan") and then prove testenv.KillPids could collect it. That
// premise is now false by construction, which is the point: the harness reap
// (testenv.PluginChildrenOf + testenv.KillPids, still wired into
// PTYSession.Close and MCPClient.Close and still exercised by this test's own
// t.Cleanup(sess.Close)) was a harness-side workaround for a product-side
// leak, and could only ever protect processes this harness itself spawned.
// Nothing protected `ctxloom run` in a developer's terminal — a real source
// of these leaks.
//
// This is the payload assertion the task asked for: process-table absence,
// not "the reap function returned without error".
//
// CTXLOOM_MOCK_ECHO_STDIN keeps the run alive reading lines off an open pty
// (internal/lm/backends/mock.go's executeInteractiveEcho: parks until "quit"
// or EOF) instead of the normal interactive `run`, whose mock-backed session
// round-trips and exits on its own on the order of 10ms (viewer_pty_test.go)
// — far too fast to reliably observe, let alone hard-kill, the plugin
// subprocess mid-flight. This is the SAME env-gated echo mode
// dockerexec_docker_integration_test.go and
// container_delivery_docker_integration_test.go already use to hold a real
// engine turn open for their own pty/exec proofs; a real pty (testenv.RunPTY,
// aymanbagabas/go-pty — the F2 binary-level harness viewer_pty_test.go
// established) is what makes the CLI take the interactive path at all
// (internal/cli/run_terminal.go's interactiveTerminal requires stdin to be an
// actual tty, which a plain io.Pipe is not).
func TestMockPluginReapedOnHardKilledParent(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "hardkill-fragment", nil, "hard-kill reap test content")

	sess, err := env.RunPTY(80, 24, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "-f", "hardkill-fragment")
	require.NoError(t, err, "start ctxloom run")
	// Close is the harness's own safety net (SIGTERM, escalate to SIGKILL,
	// sweep any still-living plugin child) for a failing run of THIS test —
	// registered via t.Cleanup so it always runs, but strictly AFTER the
	// require.Eventually below has already independently observed whether the
	// mechanism under test (PR_SET_PDEATHSIG) worked on its own.
	t.Cleanup(sess.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("pty output: %s", sess.Output())
		}
	})

	parentPID := sess.PID()

	// Wait for the mock plugin subprocess ("ctxloom llm serve mock",
	// internal/lm/grpc's self-invoking plugin path) to actually come up as
	// this process's child.
	var childPIDs []int
	deadline := time.Now().Add(hardKillPollTimeout)
	for time.Now().Before(deadline) {
		childPIDs = testenv.PluginChildrenOf(parentPID)
		if len(childPIDs) > 0 {
			break
		}
		time.Sleep(hardKillPollInterval)
	}
	require.NotEmpty(t, childPIDs, "mock plugin subprocess never appeared as a child of pid %d", parentPID)
	childPID := childPIDs[0]
	require.True(t, processAlive(childPID), "sanity: captured plugin pid %d isn't actually alive", childPID)

	// THE adversarial action: kill the parent hard. SIGKILL is uncatchable —
	// cmd/run.go's signal.NotifyContext(shutdownSignals) graceful path
	// (SIGTERM/SIGINT/SIGHUP -> `defer client.Kill()`) NEVER RUNS. This is
	// exactly the failure mode named in the task: kill -9, panic, a deleted
	// worktree, or an agent harness tearing the process down — the parent
	// gets no chance to reap anything itself.
	require.NoError(t, syscall.Kill(parentPID, syscall.SIGKILL))
	exited, _ := sess.Wait(hardKillPollTimeout) // reap the zombie; the (SIGKILL) exit error is expected and irrelevant
	require.True(t, exited, "parent process %d was not reaped within %s of SIGKILL", parentPID, hardKillPollTimeout)

	// NOTHING IS DONE HERE ON PURPOSE. No KillPids, no signal, no sweep —
	// the harness deliberately abandons the plugin child exactly as a dying
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
		"mock plugin subprocess pid %d outlived its hard-killed parent %d with nothing left to reap it — this is the orphaned-`llm serve mock` leak", childPID, parentPID)
}

// processAlive reports whether pid names a live process, via the standard
// POSIX kill(pid, 0) existence probe (no signal actually sent).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
