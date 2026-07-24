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

// TestKillSession_ReapsOrphanedGrandchild is damp-pupil 3's regression test:
// a go-plugin runner spawned via setsid, then HARD-killed directly (exactly
// go-plugin's raw cmd.Process.Kill() fallback in github.com/hashicorp/go-plugin, or
// an operator's/OOM-killer's kill -9 on the runner pid — the graceful RPC
// path never runs either way) orphans a grandchild it isolated into its own
// process group, mirroring internal/acp's setpgid'd claude-code-acp
// (moral-scorn). A plain single-pid kill of the runner never reaches that
// grandchild — proven below BEFORE killSession is invoked, so the failure
// mode is on the record, not assumed. killSession, given the runner's pid
// (== its session id, since it was spawned via setsid), reaps it.
func TestKillSession_ReapsOrphanedGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/child.pid"

	runner := exec.Command(os.Args[0], "-test.run=TestHelperKillSessionRunner", "--", pidFile)
	runner.Env = append(os.Environ(), "CTXLOOM_GRPC_HELPER_PROCESS=1")
	setsid(runner)
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
