//go:build integration && !windows

package integration

import (
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// syncBuffer is a mutex-guarded byte accumulator: cmd.Stdout/Stderr are
// written by a background io.Copy goroutine os/exec starts internally (for
// any non-*os.File writer) that keeps running until Wait returns, while
// t.Cleanup may read the buffer for a failure-path debug dump — a plain
// bytes.Buffer would race between those two goroutines whenever Wait is
// reached only via the safety-net cleanup instead of the main flow.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

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
// pdeath_linux.go) — 208 in one incident, 36 more on 2026-07-24 across three
// checkouts, some running unattended for over 36 hours.
//
// This test USED to assert the opposite as its premise ("the plugin child is
// now an orphan") and then prove testenv.KillPids could collect it. That
// premise is now false by construction, which is the point: the harness reap
// (testenv.PluginChildrenOf + testenv.KillPids, still wired into
// PTYSession.Close and MCPClient.Close and still exercised by this test's
// safety-net cleanup) was a harness-side workaround for a product-side leak,
// and could only ever protect processes this harness itself spawned. Nothing
// protected `ctxloom run` in a developer's terminal — which is where 12 of
// the 36 came from.
//
// This is the payload assertion the task asked for: process-table absence,
// not "the reap function returned without error".
//
// --structured keeps the run alive reading messages off an open stdin pipe
// (run_structured.go: parks until EOF) instead of the normal interactive
// `run`, whose mock-backed session round-trips and exits on its own on the
// order of 10ms (viewer_pty_test.go) — far too fast to reliably observe,
// let alone hard-kill, the plugin subprocess mid-flight. The mock backend's
// Chat implementation (mock_chat.go) blocks on that same stdin channel, so
// this deterministically holds BOTH the outer ctxloom process and its
// spawned "llm serve mock" plugin subprocess open until this test acts.
func TestMockPluginReapedOnHardKilledParent(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "hardkill-fragment", nil, "hard-kill reap test content")

	cmd := env.Command(nil, "run", "--structured", "-f", "hardkill-fragment")
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	cmd.Stdin = stdinR
	outBuf := &syncBuffer{}
	errBuf := &syncBuffer{}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	require.NoError(t, cmd.Start(), "start ctxloom run --structured")
	parentPID := cmd.Process.Pid
	waited := false
	// Safety net: if an assertion below fails (require's t.FailNow ends this
	// goroutine via runtime.Goexit before reaching the explicit Wait further
	// down), still reap both the parent zombie and — via the same
	// PluginChildrenOf/KillPids pair — any plugin child, so a failing run of
	// THIS test doesn't itself leak a "llm serve mock". Registered before the
	// debug-log cleanup so it runs first (t.Cleanup is LIFO): the log below
	// can then report the final captured output.
	//
	// capturedChildren is filled in once the plugin child has been observed,
	// and reaped unconditionally — INCLUDING after `waited`. The final
	// assertion runs after the parent is already dead and deliberately does
	// nothing itself; on the failing path (the fix regressed, the child
	// outlived its parent) the child is by definition an orphan with nothing
	// else in the world left to collect it. An earlier version of this
	// cleanup returned early on `waited` and so leaked exactly one orphaned
	// mock per red run — the very population it was written to protect.
	var capturedChildren []int
	t.Cleanup(func() {
		defer func() { testenv.KillPids(capturedChildren) }()
		if waited {
			return
		}
		strayChildren := testenv.PluginChildrenOf(parentPID)
		_ = cmd.Process.Kill()
		_ = stdinW.Close() // unpark the internal stdin-copy goroutine — see below
		_ = cmd.Wait()
		testenv.KillPids(strayChildren)
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stdout: %s", outBuf.String())
			t.Logf("stderr: %s", errBuf.String())
		}
	})

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
	capturedChildren = childPIDs // arm the cleanup's unconditional reap
	childPID := childPIDs[0]
	require.True(t, processAlive(childPID), "sanity: captured plugin pid %d isn't actually alive", childPID)

	// THE adversarial action: kill the parent hard. SIGKILL is uncatchable —
	// cmd/run.go's signal.NotifyContext(shutdownSignals) graceful path
	// (SIGTERM/SIGINT/SIGHUP -> `defer client.Kill()`) NEVER RUNS. This is
	// exactly the failure mode named in the task: kill -9, panic, a deleted
	// worktree, or an agent harness tearing the process down — the parent
	// gets no chance to reap anything itself.
	require.NoError(t, syscall.Kill(parentPID, syscall.SIGKILL))
	// exec.Cmd's internal stdin-copy goroutine (spawned because Stdin is a
	// plain io.Reader, not an *os.File) is parked reading from stdinR and
	// never observes the child's death on its own — only EOF or a read
	// error unparks it, and Wait() (below and in the safety-net cleanup
	// above) blocks until it does. Close the writer now so Wait() actually
	// returns instead of hanging — this is a test-harness-only gotcha (a
	// plain os/exec fact about non-*os.File Stdin), unrelated to the reap
	// mechanism under test.
	_ = stdinW.Close()
	_ = cmd.Wait() // reap the zombie; the (SIGKILL) exit error is expected and irrelevant
	waited = true

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
