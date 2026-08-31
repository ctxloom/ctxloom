package acpagent

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fsUpstreamListener.Close removes its temp directory, and a unit test already
// pins that. The leak this reaper exists for is that Close is not always
// REACHED: its only caller is teardownSession, via closeAllSessions, which runs
// as `defer s.closeAllSessions()` inside Serve. A defer does not run when the
// process is killed — and being killed is the normal case here, since an editor
// spawns `ctxloom acp serve` as its own child and kills it.
//
// Measured on this machine before the reaper existed: 2,571 orphaned
// ctxloom-acp-fs-* directories in TMPDIR, oldest 2026-08-17, newest created
// seven minutes before the measurement, all with dead sockets.
//
// No in-process mechanism survives SIGKILL, so this is a SWEEP AT STARTUP,
// matching mcp.reapStaleDiscoveryMarkers and isolation.ReapOrphanedWorktrees.

// dialable reports whether something is actually listening, which is the same
// probe the reaper uses. Kept in the test so the test does not simply restate
// the implementation's own helper.
func dialable(t *testing.T, sock string) bool {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// newFsUpstreamDir builds one ctxloom-acp-fs-* directory holding a socket,
// optionally with something listening on it.
func newFsUpstreamDir(t *testing.T, root string, live bool) (dir, sock string, closer func()) {
	t.Helper()
	dir, err := os.MkdirTemp(root, fsUpstreamDirPrefix)
	require.NoError(t, err)
	sock = filepath.Join(dir, "fs.sock")
	if !live {
		require.NoError(t, os.WriteFile(sock, nil, 0o600))
		return dir, sock, func() {}
	}
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	return dir, sock, func() { _ = ln.Close() }
}

// TestReapStaleFsUpstreams_SparesALiveListener is THE test. A reaper that
// removes everything passes any "the dead one is gone" assertion, so sparing a
// LIVE socket is the property that actually constrains it — and it is the one
// whose failure destroys a running editor's fs channel rather than merely
// leaving litter.
func TestReapStaleFsUpstreams_SparesALiveListener(t *testing.T) {
	root := t.TempDir()
	dir, sock, closer := newFsUpstreamDir(t, root, true)
	defer closer()
	require.True(t, dialable(t, sock), "precondition: the listener must actually be up")

	res := ReapStaleFsUpstreams(root, time.Now().Add(time.Hour))

	assert.DirExists(t, dir, "a live fs-upstream must never be reaped")
	assert.Equal(t, 0, res.Reaped, "nothing was dead")
	assert.Equal(t, 1, res.Spared)
}

// TestReapStaleFsUpstreams_ReapsADeadSocket is the leak itself: the directory
// an abandoned process left behind.
func TestReapStaleFsUpstreams_ReapsADeadSocket(t *testing.T) {
	root := t.TempDir()
	dir, sock, _ := newFsUpstreamDir(t, root, false)
	require.False(t, dialable(t, sock), "precondition: nothing may be listening")
	require.DirExists(t, dir, "precondition: the directory exists before the sweep")

	res := ReapStaleFsUpstreams(root, time.Now().Add(time.Hour))

	assert.NoDirExists(t, dir, "an orphaned fs-upstream directory must be removed")
	assert.Equal(t, 1, res.Reaped)
}

// TestReapStaleFsUpstreams_SkipsWithinTheGraceWindow: a listener that has been
// created but has not bound yet looks exactly like a dead one. Reaping it would
// delete the socket path out from under a process that is still starting.
func TestReapStaleFsUpstreams_SkipsWithinTheGraceWindow(t *testing.T) {
	root := t.TempDir()
	dir, _, _ := newFsUpstreamDir(t, root, false)

	// now == the directory's own mtime, so it is inside any grace window.
	res := ReapStaleFsUpstreams(root, time.Now())

	assert.DirExists(t, dir, "a just-created fs-upstream may still be binding")
	assert.Equal(t, 0, res.Reaped)
	assert.Equal(t, 1, res.Skipped)
}

// TestReapStaleFsUpstreams_TouchesNothingElse: this deletes directories in a
// SHARED tmpdir. Anything not matching the prefix is not ours, whatever state
// it is in.
func TestReapStaleFsUpstreams_TouchesNothingElse(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "someone-elses-dir")
	require.NoError(t, os.MkdirAll(foreign, 0o755))
	keep := filepath.Join(root, "ctxloom-acp-fs-not-a-dir")
	require.NoError(t, os.WriteFile(keep, []byte("x"), 0o600))

	res := ReapStaleFsUpstreams(root, time.Now().Add(time.Hour))

	assert.DirExists(t, foreign, "a directory that is not ours must never be touched")
	assert.FileExists(t, keep, "a FILE matching the prefix is not a fs-upstream directory")
	assert.Equal(t, 0, res.Reaped)

	// NOT MERELY "it survived" — a prefix-matching FILE must not be a
	// candidate at all. Without the IsDir guard it still survives, because
	// stat'ing <file>/fs.sock fails with ENOTDIR and that is reported as
	// UNKNOWN rather than dead; it would just be counted as skipped. Asserting
	// only survival therefore cannot tell the guard from its absence — that
	// mutation SURVIVED until this line existed. Counting is what distinguishes
	// "never considered" from "considered and let off".
	assert.Equal(t, 0, res.Skipped,
		"a file matching the prefix must not even be examined as a candidate")
	assert.Equal(t, 0, res.Spared)
}

// TestReapStaleFsUpstreams_MissingRootIsNotAnError: a startup sweep is
// fault-tolerant by contract — it must never be able to stop the agent
// starting.
func TestReapStaleFsUpstreams_MissingRootIsNotAnError(t *testing.T) {
	res := ReapStaleFsUpstreams(filepath.Join(t.TempDir(), "does-not-exist"), time.Now())
	assert.Equal(t, 0, res.Reaped)
	assert.Equal(t, 0, res.Spared)
}
