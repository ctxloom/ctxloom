//go:build !windows

package procalive

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlive_LiveChild_ReadsAlive pins the non-permissive direction: a
// genuinely still-running process must read Alive. This is what proves the
// zombie carve-out did not make the check trivially permissive — a check
// that always answered false would also pass a zombie-reads-dead test, so
// that test alone proves nothing without this one alongside it.
func TestAlive_LiveChild_ReadsAlive(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	assert.True(t, Alive(cmd.Process.Pid), "a genuinely live child must read Alive")
}

// TestAlive_UnreapedZombie_ReadsDead is the regression this package exists
// for. A child that has exited but has not yet been wait(2)-ed by its
// parent is a zombie: present in the process table, still a signalable pid,
// and syscall.Kill(pid, 0) alone reports it Alive. That is exactly the shape
// of an orphan reparented to a non-reaping PID 1 in a container — the
// process exited, nobody has called wait on it, and it lingers looking
// alive to a bare kill(pid,0) probe.
//
// This spawns a real child, lets it exit, and deliberately does NOT call
// cmd.Wait() before checking Alive, so the pid is a genuine zombie under
// THIS test process — the same mechanism a non-reaping PID 1 leaves behind,
// produced here without needing an actual PID namespace.
func TestAlive_UnreapedZombie_ReadsDead(t *testing.T) {
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	// Deliberately not calling cmd.Wait() yet: the child exits on its own,
	// but nothing has reaped it, so it becomes and stays a zombie until this
	// test calls Wait() below.
	require.Eventually(t, func() bool {
		return isZombie(pid)
	}, 2*time.Second, 5*time.Millisecond,
		"child pid %d never reached zombie state — this fixture is not exercising what the test claims", pid)

	// Sanity check on the PREMISE, not the fix: confirm the bare kill(pid,0)
	// probe this package replaces really does report the zombie alive. If
	// this ever stopped being true the test above would prove nothing about
	// the bug being fixed.
	assert.NoError(t, syscall.Kill(pid, 0),
		"sanity: kill(pid,0) alone must still succeed against a zombie, or this fixture proves nothing about the bug being fixed")

	assert.False(t, Alive(pid), "an unreaped zombie must read as dead, not alive")

	require.NoError(t, cmd.Wait())
}
