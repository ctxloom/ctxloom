//go:build !windows

package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/parentwatch"
	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
)

// TestHelperKillSessionRunner is not a real test — it is the re-exec target
// TestKillSession_ReapsOrphanedGrandchild spawns via os.Args[0] (the
// standard library's own TestHelperProcess idiom, os/exec_test.go). Guarded
// by an env var so `go test` running it directly (as an ordinary test) is an
// instant no-op. It plays the part of a runner: spawns a grandchild in its
// OWN process group, records that pid, then blocks.
func TestHelperKillSessionRunner(t *testing.T) {
	if os.Getenv("CTXLOOM_GRPC_HELPER_PROCESS") != "1" {
		return
	}
	pidFile := os.Args[len(os.Args)-1]
	child := exec.Command("sleep", "100")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper: start child:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write pidfile:", err)
		os.Exit(1)
	}
	time.Sleep(100 * time.Second)
}

// processAlive reports whether pid denotes a still-running process (not a
// zombie). The zombie carve-out matters under an unreaped-ancestor
// container, where a reaped-by-nobody process lingers in the table and
// kill(pid, 0) still succeeds against it. The actual check lives in
// internal/testsupport/procalive, shared with tests/integration's own reap
// test so the two cannot drift apart again.
func processAlive(pid int) bool {
	return procalive.Alive(pid)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

// TestKillSession_ReapsOrphanedGrandchild is a regression test: a runner
// spawned via isolateRunner, then HARD-killed directly (a raw
// cmd.Process.Kill(), or an operator's/OOM-killer's kill -9 on the runner
// pid — the runner's own teardown never runs either way) orphans a
// grandchild it isolated into its own process group. A plain single-pid kill of the runner never reaches that
// grandchild — proven below BEFORE killSession is invoked, so the failure
// mode is on the record, not assumed. killSession, given the runner's pid
// (== its session id, since it was spawned via isolateRunner), reaps it.
func TestKillSession_ReapsOrphanedGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/child.pid"

	runner := exec.Command(os.Args[0], "-test.run=TestHelperKillSessionRunner", "--", pidFile)
	runner.Env = append(os.Environ(), "CTXLOOM_GRPC_HELPER_PROCESS=1")
	isolateRunner(runner)
	require.NoError(t, runner.Start())
	runnerPID := runner.Process.Pid
	t.Cleanup(func() {
		_ = runner.Process.Kill()
		_ = runner.Wait()
	})

	childPID, err := strconv.Atoi(waitForFile(t, pidFile, 5*time.Second))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return processAlive(childPID) }, time.Second, 10*time.Millisecond,
		"the grandchild must actually be running before we can prove anything about reaping it")

	// The pre-fix failure mode, on the record: killing ONLY the runner's own
	// pid (what the graceful path degrades to on any hard kill) never reaches a grandchild the runner
	// put in its own process group.
	require.NoError(t, syscall.Kill(runnerPID, syscall.SIGKILL))
	require.Eventually(t, func() bool { return !processAlive(runnerPID) }, time.Second, 10*time.Millisecond,
		"sanity: the runner itself must actually be dead before checking the grandchild")
	require.True(t, processAlive(childPID),
		"sanity/documentation: a plain kill of just the runner pid leaves the grandchild running — this IS damp-pupil 3, the bug killSession exists to fix")

	killSession(runnerPID)
	require.Eventually(t, func() bool { return !processAlive(childPID) }, 2*time.Second, 10*time.Millisecond,
		"killSession must reap the runner's whole session, including a grandchild the runner isolated into its own process group")
}

// TestHelperRunnerHost is not a real test — it is the re-exec target
// assertRunnerDiesWithItsHost spawns via os.Args[0] (the same
// TestHelperProcess idiom as TestHelperKillSessionRunner above). It plays
// the part of the ctxloom HOST process — the `ctxloom run` / `ctxloom mcp`
// that self-execs its runner — spawning ONE child, the argv after the pid
// file, with exactly the production runner attributes (isolateRunner, what
// StartHostRunner applies), recording its pid, then blocking as a live host
// would.
func TestHelperRunnerHost(t *testing.T) {
	if os.Getenv("CTXLOOM_GRPC_HOST_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	pidFile, argv := args[0], args[1:]
	runner := exec.Command(argv[0], argv[1:]...)
	isolateRunner(runner)
	if err := runner.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper: start runner:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(runner.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write pidfile:", err)
		os.Exit(1)
	}
	time.Sleep(100 * time.Second)
}

// TestHelperWatchingRunner is not a real test — it is the runner
// TestIsolateRunner_RunnerDiesWithItsHost hands the host helper: our own
// binary, arming the in-child parent watch exactly as runRunner does, then
// living until that watch fires.
func TestHelperWatchingRunner(t *testing.T) {
	armedFile := os.Getenv("CTXLOOM_WATCHING_RUNNER_ARMED")
	if armedFile == "" {
		return
	}
	ctx, cancel, err := parentwatch.WithParent(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: arm parent watch:", err)
		os.Exit(1)
	}
	defer cancel()
	if err := os.WriteFile(armedFile, []byte("armed"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write armed file:", err)
		os.Exit(1)
	}
	select {
	case <-ctx.Done():
		os.Exit(0)
	case <-time.After(100 * time.Second):
		os.Exit(1)
	}
}

// TestIsolateRunner_RunnerDiesWithItsHost is the regression test for the
// leaked-runner defect: `ctxloom llm serve mock --label mock` processes have
// been found reparented to init, some running unattended for over 36 hours,
// across checkouts including deleted git worktrees — a recurring failure
// mode, not a one-off.
//
// The runner here is our own binary arming parentwatch.WithParent, as the
// production runner does, so this passes on every unix: through
// PR_SET_PDEATHSIG on Linux, through the kqueue watch on darwin/BSD. The
// foreign-binary case, which only the kernel attribute can cover, is
// TestIsolateRunner_ForeignRunnerDiesWithItsHost (Linux-only).
func TestIsolateRunner_RunnerDiesWithItsHost(t *testing.T) {
	// The runner reports that its watch is armed, so a helper that never got
	// that far (and exited on its own) cannot pass for one the watch ended.
	armed := t.TempDir() + "/armed"
	t.Setenv("CTXLOOM_WATCHING_RUNNER_ARMED", armed)
	assertRunnerDiesWithItsHost(t, armed, os.Args[0], "-test.run=^TestHelperWatchingRunner$")
}

// assertRunnerDiesWithItsHost starts a host that spawns runnerArgv under
// isolateRunner, then SIGKILLs the host and requires the runner gone. A
// non-empty armedFile is one the runner writes once it is ready to be
// judged; the host is not killed before it exists.
//
// The host dies WITHOUT running its own teardown — a SIGKILL here, which is
// equally `go test -timeout`'s escalation, an OOM kill, a killed shell that
// took its whole process group with it, a panic that skipped every defer, or
// a cobra path that called os.Exit. HostRunner.Kill/killSession live INSIDE
// that host process and cannot run once it is gone, and isolateRunner has
// deliberately put the runner in its own session, out of reach of any group
// signal. Only the kernel (Pdeathsig) or the runner itself (parentwatch)
// still knows the relationship.
//
// Asserts ABSENCE from the process table, not that some cleanup func
// returned: a teardown that reports success while the process keeps running
// is precisely this defect.
func assertRunnerDiesWithItsHost(t *testing.T, armedFile string, runnerArgv ...string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := dir + "/runner.pid"

	host := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperRunnerHost$", "--", pidFile}, runnerArgv...)...)
	host.Env = append(os.Environ(), "CTXLOOM_GRPC_HOST_HELPER=1")
	require.NoError(t, host.Start())
	hostPID := host.Process.Pid
	t.Cleanup(func() {
		_ = host.Process.Kill()
		_ = host.Wait()
	})

	runnerPID, err := strconv.Atoi(waitForFile(t, pidFile, 5*time.Second))
	require.NoError(t, err)
	// This test's OWN leak guard: if the assertion below fails, the runner is
	// by definition still running and reparented to init, and nothing else
	// would ever collect it. Kill it by the exact pid we spawned — never a
	// name-matched sweep, which on a box running several suites at once would
	// reach other people's fixtures (and, with pkill -f, the killing shell).
	t.Cleanup(func() { _ = syscall.Kill(runnerPID, syscall.SIGKILL) })

	if armedFile != "" {
		waitForFile(t, armedFile, 5*time.Second)
	}
	require.Eventually(t, func() bool { return processAlive(runnerPID) }, time.Second, 10*time.Millisecond,
		"the runner must actually be running before we can prove anything about reaping it")

	require.NoError(t, syscall.Kill(hostPID, syscall.SIGKILL))
	_ = host.Wait()
	require.Eventually(t, func() bool { return !processAlive(hostPID) }, 2*time.Second, 10*time.Millisecond,
		"sanity: the host itself must actually be dead before checking the runner")

	require.Eventually(t, func() bool { return !processAlive(runnerPID) }, 5*time.Second, 20*time.Millisecond,
		"the runner must be GONE from the process table once its host dies — a host killed without running its teardown is exactly how the 36 orphaned `llm serve mock` processes accumulated")
}
