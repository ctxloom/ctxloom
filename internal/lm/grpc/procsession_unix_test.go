//go:build !windows

package grpc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHelperKillSessionRunner is not a real test — it is the re-exec target
// TestKillSession_ReapsOrphanedGrandchild spawns via os.Args[0] (the
// standard library's own TestHelperProcess idiom, os/exec_test.go). Guarded
// by an env var so `go test` running it directly (as an ordinary test) is an
// instant no-op. It plays the part of a go-plugin runner: spawns a
// grandchild in its OWN process group (mirroring internal/acp's setpgid'd
// claude-code-acp, moral-scorn), records that pid, then blocks — like a
// runner sitting inside plugin.Serve().
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
// zombie) — see internal/acp/procgroup_unix_test.go's identical helper for
// why the zombie carve-out matters under an unreaped-ancestor container.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	return !isZombie(pid)
}

func isZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == ')' && i+2 < len(data) {
			return data[i+2] == 'Z'
		}
	}
	return false
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

// TestKillSession_ReapsOrphanedGrandchild is a regression test: a go-plugin
// runner spawned via isolateRunner, then HARD-killed directly (exactly
// go-plugin's raw cmd.Process.Kill() fallback in github.com/hashicorp/go-plugin, or
// an operator's/OOM-killer's kill -9 on the runner pid — the graceful RPC
// path never runs either way) orphans a grandchild it isolated into its own
// process group, mirroring internal/acp's setpgid'd claude-code-acp.
// A plain single-pid kill of the runner never reaches that
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
	// pid (what go-plugin's raw fallback does, and what the graceful path
	// degrades to on any hard kill) never reaches a grandchild the runner
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
// TestIsolateRunner_RunnerDiesWithItsHost spawns via os.Args[0] (the same
// TestHelperProcess idiom as TestHelperKillSessionRunner above). It plays
// the part of the ctxloom HOST process — the `ctxloom run` / `ctxloom mcp`
// that self-execs `ctxloom llm serve <backend>` — spawning ONE child with
// exactly the production runner attributes (isolateRunner, what
// dialLLMConnection and StartHostRunner apply), recording its pid, then
// blocking as a live host would.
func TestHelperRunnerHost(t *testing.T) {
	if os.Getenv("CTXLOOM_GRPC_HOST_HELPER") != "1" {
		return
	}
	pidFile := os.Args[len(os.Args)-1]
	runner := exec.Command("sleep", "100")
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

// TestIsolateRunner_RunnerDiesWithItsHost is the regression test for the
// leaked-runner defect: `ctxloom llm serve mock --label mock` processes have
// been found reparented to init, some running unattended for over 36 hours,
// across checkouts including deleted git worktrees — a recurring failure
// mode, not a one-off.
//
// The host dies WITHOUT running its own teardown — a SIGKILL here, which is
// equally `go test -timeout`'s escalation, an OOM kill, a killed shell that
// took its whole process group with it, a panic that skipped every defer, or
// a cobra path that called os.Exit. realLLMConnection.Kill/killSession live
// INSIDE that host process and cannot run once it is gone, and the runner
// has no independent way to notice: go-plugin gives it the HOST's stdin
// rather than a pipe (github.com/hashicorp/go-plugin client.go's
// `cmd.Stdin = os.Stdin`), so no EOF ever arrives, and isolateRunner has
// deliberately put it in its own session, out of reach of any group signal.
// Nothing else is watching. The kernel is the only party that still knows
// the relationship after the host is gone, which is why the fix is
// PR_SET_PDEATHSIG rather than more userspace bookkeeping.
//
// Asserts ABSENCE from the process table, not that some cleanup func
// returned: a teardown that reports success while the process keeps running
// is precisely this defect.
func TestIsolateRunner_RunnerDiesWithItsHost(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/runner.pid"

	host := exec.Command(os.Args[0], "-test.run=TestHelperRunnerHost", "--", pidFile)
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

	require.Eventually(t, func() bool { return processAlive(runnerPID) }, time.Second, 10*time.Millisecond,
		"the runner must actually be running before we can prove anything about reaping it")

	require.NoError(t, syscall.Kill(hostPID, syscall.SIGKILL))
	_ = host.Wait()
	require.Eventually(t, func() bool { return !processAlive(hostPID) }, 2*time.Second, 10*time.Millisecond,
		"sanity: the host itself must actually be dead before checking the runner")

	require.Eventually(t, func() bool { return !processAlive(runnerPID) }, 5*time.Second, 20*time.Millisecond,
		"the runner must be GONE from the process table once its host dies — a host killed without running its teardown is exactly how the 36 orphaned `llm serve mock` processes accumulated")
}

// TestHelperTeardownRunner is not a real test — it is the re-exec target
// TestInstallRunnerTeardown_ReapsEngineOnParentDeath spawns via os.Args[0].
// It plays the part of `ctxloom llm serve <backend>`: optionally installs the
// production teardown, spawns an engine subprocess in its OWN process group
// (mirroring internal/acp's setpgid'd claude-code-acp), records that pid, then
// blocks the way plugin.Serve does.
//
// CTXLOOM_GRPC_TEARDOWN selects the arm: "0" reproduces the pre-fix runner
// (SIGTERM's default disposition, no sweep), "1" the fixed one.
func TestHelperTeardownRunner(t *testing.T) {
	if os.Getenv("CTXLOOM_GRPC_TEARDOWN_HELPER") != "1" {
		return
	}
	if os.Getenv("CTXLOOM_GRPC_TEARDOWN") == "1" {
		InstallRunnerTeardown()
	}
	pidFile := os.Args[len(os.Args)-1]
	engine := exec.Command("sleep", "100")
	engine.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := engine.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper: start engine:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(engine.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write pidfile:", err)
		os.Exit(1)
	}
	time.Sleep(100 * time.Second)
}

// startTeardownRunner spawns TestHelperTeardownRunner as a session leader
// through the production isolateRunner, waits for its engine grandchild to
// exist, and returns both pids. Both are unconditionally reaped by pid on test
// exit so a failing arm can never itself contribute to the orphan population
// this whole change exists to end.
func startTeardownRunner(t *testing.T, installTeardown bool) (runnerPID, enginePID int) {
	t.Helper()
	pidFile := t.TempDir() + "/engine.pid"
	runner := exec.Command(os.Args[0], "-test.run=TestHelperTeardownRunner", "--", pidFile)
	runner.Env = append(os.Environ(),
		"CTXLOOM_GRPC_TEARDOWN_HELPER=1",
		"CTXLOOM_GRPC_TEARDOWN="+map[bool]string{true: "1", false: "0"}[installTeardown],
	)
	isolateRunner(runner)
	require.NoError(t, runner.Start())
	runnerPID = runner.Process.Pid
	t.Cleanup(func() {
		_ = runner.Process.Kill()
		_ = runner.Wait()
	})

	var err error
	enginePID, err = strconv.Atoi(waitForFile(t, pidFile, 5*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(enginePID, syscall.SIGKILL) })

	require.Eventually(t, func() bool { return processAlive(enginePID) }, time.Second, 10*time.Millisecond,
		"the engine subprocess must actually be running before we can prove anything about reaping it")
	return runnerPID, enginePID
}

// TestInstallRunnerTeardown_ReapsEngineOnParentDeath covers the step AFTER
// isolateRunner's Pdeathsig fires. The kernel signals the runner SIGTERM when
// its host dies; left to SIGTERM's default disposition that ends the runner
// and strands the engine subprocess it had isolated into its own process
// group — trading an orphaned runner for an orphaned engine, not closing the
// leak. `llm serve` has no other graceful path (its body is go-plugin's
// blocking plugin.Serve), so InstallRunnerTeardown is what makes SIGTERM mean
// "take your subtree with you".
//
// Both arms run: the pre-fix one is asserted, not assumed, so the failure mode
// stays on the record the way TestKillSession_ReapsOrphanedGrandchild keeps
// damp-pupil 3's.
func TestInstallRunnerTeardown_ReapsEngineOnParentDeath(t *testing.T) {
	t.Run("without teardown the engine is stranded", func(t *testing.T) {
		runnerPID, enginePID := startTeardownRunner(t, false)

		require.NoError(t, syscall.Kill(runnerPID, syscall.SIGTERM))
		require.Eventually(t, func() bool { return !processAlive(runnerPID) }, 2*time.Second, 10*time.Millisecond,
			"sanity: SIGTERM's default disposition must actually end the runner")
		require.True(t, processAlive(enginePID),
			"sanity/documentation: a runner that merely dies on SIGTERM leaves its engine subprocess running — this is the half of the leak Pdeathsig alone does not close")
	})

	t.Run("with teardown the engine goes too", func(t *testing.T) {
		runnerPID, enginePID := startTeardownRunner(t, true)

		require.NoError(t, syscall.Kill(runnerPID, syscall.SIGTERM))
		require.Eventually(t, func() bool { return !processAlive(runnerPID) }, 2*time.Second, 10*time.Millisecond,
			"sanity: the runner itself must actually be dead before checking the engine")
		require.Eventually(t, func() bool { return !processAlive(enginePID) }, 5*time.Second, 20*time.Millisecond,
			"InstallRunnerTeardown must sweep the runner's whole session on SIGTERM, so nothing below it survives the host's death")
	})
}

// TestReapRunnerDescendants_RefusesOutsideItsOwnSession is the blast-radius
// guard. The test binary is NOT a session leader (it shares the session of the
// shell or `go test` process that started it), so calling the sweep here must
// be a no-op — if it were not, it would SIGKILL that whole session: the
// developer's shell, its job control, every sibling command, and on this
// machine other agents' concurrently running suites. That is the difference
// between a scoped reap and the `pkill -f` pattern this change exists to avoid.
func TestReapRunnerDescendants_RefusesOutsideItsOwnSession(t *testing.T) {
	self := os.Getpid()
	require.NotEqual(t, self, procSessionID(self),
		"precondition: the test binary must not be its own session leader for this guard to be under test")

	bystander := exec.Command("sleep", "100")
	require.NoError(t, bystander.Start())
	bystanderPID := bystander.Process.Pid
	t.Cleanup(func() {
		_ = bystander.Process.Kill()
		_ = bystander.Wait()
	})
	require.Eventually(t, func() bool { return processAlive(bystanderPID) }, time.Second, 10*time.Millisecond)

	ReapRunnerDescendants()

	require.True(t, processAlive(bystanderPID),
		"ReapRunnerDescendants must refuse to sweep a session it does not lead — a process in the caller's own session was killed")
	require.True(t, processAlive(self), "and it must certainly not have killed the test binary")
}
