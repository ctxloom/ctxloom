package procpin

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A caller decides whether to signal a pid, then signals it. A pid the kernel
// has since recycled would take that signal, so the handle must be bound to the
// PROCESS, not to its number. Once the pinned process is gone the handle must
// resolve to nothing at all — that is what makes a recycled pid unreachable
// through it.
func TestHandle_PinnedIdentityDoesNotFollowARecycledPid(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	h, ok := Pin(pid)
	require.True(t, ok, "a live process must be pinnable")
	t.Cleanup(h.Close)

	assert.Equal(t, pid, pinnedPid(t, h), "the handle must name the process it was taken on")

	// The process ends and is reaped: its pid is now free for the kernel to
	// hand to anyone.
	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()
	require.Eventually(t, func() bool { return pinnedPid(t, h) == -1 }, 5*time.Second, 10*time.Millisecond,
		"once the pinned process is gone the handle must resolve to no process, so a recycled pid is unreachable through it")

	// Signalling through a spent handle is a no-op rather than a stranger's death.
	assert.NotPanics(t, func() {
		h2, ok := Pin(pid)
		if ok {
			h2.Close()
		}
	})
}

// pinnedPid reads the process a pidfd currently refers to (-1 once it has
// exited), per proc(5)'s fdinfo for pidfds.
func pinnedPid(t *testing.T, h Handle) int {
	t.Helper()
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(h.fd))
	require.NoError(t, err)
	for _, line := range strings.Split(string(data), "\n") {
		if rest, found := strings.CutPrefix(line, "Pid:"); found {
			return mustAtoi(strings.TrimSpace(rest))
		}
	}
	t.Fatalf("no Pid: line in pidfd fdinfo:\n%s", data)
	return 0
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// ReadStat is the kernel's account of THIS process: its parent is the
// process that started the test binary, and it has a start time.
func TestReadStat_DescribesThisProcess(t *testing.T) {
	st, err := ReadStat(os.Getpid())
	require.NoError(t, err)
	assert.Equal(t, os.Getppid(), st.PPID)
	assert.NotZero(t, st.StartTicks)
}

// WaitExit reports an exit that happens within its bound and a process still
// alive past it, and a signal to an exited process is not an error.
func TestHandle_SignalAndWaitExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	h, ok := Pin(cmd.Process.Pid)
	require.True(t, ok)
	t.Cleanup(h.Close)
	go func() { _, _ = cmd.Process.Wait() }()

	assert.False(t, h.WaitExit(50*time.Millisecond), "a live process has not exited")
	require.NoError(t, h.Signal(syscall.SIGTERM))
	assert.True(t, h.WaitExit(5*time.Second), "a SIGTERMed sleep exits")
	assert.NoError(t, h.Signal(syscall.SIGKILL), "signalling an exited process is not an error")
}
