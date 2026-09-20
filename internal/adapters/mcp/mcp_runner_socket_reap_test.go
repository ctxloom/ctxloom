package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Graceful cleanup already unlinks a runner's socket, so every socket that
// LEAKS belongs to a process that died without one — SIGKILL, a crash, an OOM
// kill. Nothing in the shutdown path can reach those, so the next startup has
// to. Measured before this existed: 297 dead sockets against 1 live.
//
// The safety property matters more than the reap: removing a LIVE runner's
// socket severs the only route to a working session, which is far worse than
// the leak. So the live case is asserted as hard as the dead one.
func TestReapDeadRunnerSockets(t *testing.T) {
	dir := t.TempDir()
	deadPid := deadProcessPID(t)

	touch := func(t *testing.T, name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, nil, 0o600))
		return p
	}

	dead := touch(t, "mcp-"+strconv.Itoa(deadPid)+".sock")
	live := touch(t, "mcp-"+strconv.Itoa(os.Getpid())+".sock")
	// Files this sweep does not own. A name it cannot parse belongs to some
	// other writer, and guessing is how a cleanup destroys a stranger's data.
	marker := touch(t, "cell-aaaaaaaaaaaaaaaaaaaa.json")
	notASocket := touch(t, "mcp-notapid.sock")
	otherShape := touch(t, "mcp.sock")

	removed := reapDeadRunnerSockets(dir)
	require.Equal(t, 1, removed, "exactly the dead-owner socket should be reaped")

	require.NoFileExists(t, dead, "a confirmed-dead owner's socket must be removed")
	require.FileExists(t, live, "a LIVE runner's socket must survive — removing it severs a working session")
	require.FileExists(t, marker, "discovery markers belong to the marker reaper, not this one")
	require.FileExists(t, notASocket, "an unparseable pid must be left alone, never guessed at")
	require.FileExists(t, otherShape, "the private-temp tier's mcp.sock is owned by its directory's own cleanup")

	// Idempotent: a second pass has nothing left to do and must not start
	// removing things it spared the first time.
	require.Equal(t, 0, reapDeadRunnerSockets(dir), "a second sweep must remove nothing")
	require.FileExists(t, live)
}

// deadProcessPID returns the pid of a process that has just exited and been
// reaped — a pid the liveness probe reports Dead.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())
	return pid
}
