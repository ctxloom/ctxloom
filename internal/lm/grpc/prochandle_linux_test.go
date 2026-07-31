package grpc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sweep decides whether to kill a pid, then kills it. A pid the kernel has
// since recycled would take that signal, so the handle must be bound to the
// PROCESS, not to its number. Once the pinned process is gone the handle must
// resolve to nothing at all — that is what makes a recycled pid unreachable
// through it.
func TestProcHandle_PinnedIdentityDoesNotFollowARecycledPid(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	h, ok := pinProcess(pid)
	require.True(t, ok, "a live process must be pinnable")
	t.Cleanup(h.close)

	assert.Equal(t, pid, pinnedPid(t, h), "the handle must name the process it was taken on")

	// The process ends and is reaped: its pid is now free for the kernel to
	// hand to anyone.
	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()
	require.Eventually(t, func() bool { return pinnedPid(t, h) == -1 }, 5*time.Second, 10*time.Millisecond,
		"once the pinned process is gone the handle must resolve to no process, so a recycled pid is unreachable through it")

	// Signalling through a spent handle is a no-op rather than a stranger's death.
	assert.NotPanics(t, func() {
		h2, ok := pinProcess(pid)
		if ok {
			h2.close()
		}
	})
}

// pinnedPid reads the process a pidfd currently refers to (-1 once it has
// exited), per proc(5)'s fdinfo for pidfds.
func pinnedPid(t *testing.T, h procHandle) int {
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
