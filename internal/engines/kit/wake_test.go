package kit

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const testWakeVar = "CTXLOOM_TEST_WAKE_SOCKET"

func TestSocketWake_BindRefusesWithoutTheSocketVariable(t *testing.T) {
	for _, env := range []map[string]string{{}, {testWakeVar: ""}} {
		_, err := SocketWake{EnvVar: testWakeVar}.Bind(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		require.ErrorIs(t, err, engine.ErrWakeUnbound)
		assert.Contains(t, err.Error(), testWakeVar)
	}
}

// A wake is one line, the wake text, on the session's socket.
func TestSocketWake_FirePostsTheWakeTextAsOneLine(t *testing.T) {
	dir, err := os.MkdirTemp("", "kw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "w.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan []string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(got)
			return
		}
		defer conn.Close()
		var lines []string
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		got <- lines
	}()
	w, err := SocketWake{EnvVar: testWakeVar}.Bind(context.Background(), func(k string) (string, bool) { return path, k == testWakeVar })
	require.NoError(t, err)

	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	assert.Equal(t, []string{engine.WakeText("0123456789abcdef")}, testsupport.Await(t, 10*time.Second, (<-chan []string)(got), "nothing was posted"))
}

func TestSocketWake_AnUndeliverableWakeFails(t *testing.T) {
	w, err := SocketWake{EnvVar: testWakeVar}.Bind(context.Background(), func(string) (string, bool) { return filepath.Join(t.TempDir(), "absent.sock"), true })
	require.NoError(t, err)
	require.Error(t, w.Fire(context.Background(), "0123456789abcdef"))
}

// The socket is an interactive session's alone, in its short socket root
// (engine.Session.SocketRoot), never the session home: that nests too deep
// for a unix socket path.
func TestSocketWake_PathIsTheInteractiveSessionsOwn(t *testing.T) {
	w := SocketWake{EnvVar: testWakeVar}
	s := engine.Session{Mode: engine.Interactive}
	s.Roots.SessionHome.Engine = "/" + strings.Repeat("deep/", 40)
	assert.Empty(t, w.Path(s), "no socket root, no socket")
	s.SocketRoot = "/r"
	assert.Equal(t, filepath.Join("/r", "wake.sock"), w.Path(s))
	s.Mode = engine.Structured
	assert.Empty(t, w.Path(s), "a structured session has nobody to wake")
	assert.LessOrEqual(t, len(wakeSocketName), engine.SocketLeafMax, "the leaf fits the room the contract leaves")
}

// Ensure makes the socket root an owner-only directory a socket can be
// bound in, and refuses a root that is a link: a predictable name in a
// shared temp dir is not trusted to be the session's own.
func TestSocketWake_EnsureMakesAnOwnerOnlyRoot(t *testing.T) {
	w := SocketWake{EnvVar: testWakeVar}
	s := engine.Session{Mode: engine.Interactive, SocketRoot: filepath.Join(testsupport.SocketDir(t, "ctxloom-0123456789abcdef/wake.sock"), "ctxloom-0123456789abcdef")}
	path, err := w.Ensure(s)
	require.NoError(t, err)
	assert.Equal(t, w.Path(s), path)
	st, err := os.Lstat(s.SocketRoot)
	require.NoError(t, err)
	assert.True(t, st.IsDir())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	}
	_, err = w.Ensure(s)
	require.NoError(t, err, "an existing root of the session's own is reused")

	if runtime.GOOS != "windows" {
		linked := engine.Session{Mode: engine.Interactive, SocketRoot: filepath.Join(filepath.Dir(s.SocketRoot), "linked")}
		require.NoError(t, os.Symlink(s.SocketRoot, linked.SocketRoot))
		_, err = w.Ensure(linked)
		require.Error(t, err, "a root that is a link is refused")
	}

	none, err := w.Ensure(engine.Session{Mode: engine.Structured, SocketRoot: s.SocketRoot})
	require.NoError(t, err)
	assert.Empty(t, none, "a structured session needs no socket")
}

// ReleaseSocketRoot takes the session's socket root once its engine has
// gone, but only when empty: a socket still in it belongs to a process of
// the same session that is still running (a concurrent resume), whose wake
// must keep working.
func TestReleaseSocketRoot_RemovesOnlyAnEmptyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ctxloom-0123456789abcdef")
	require.NoError(t, os.Mkdir(root, 0o700))
	ReleaseSocketRoot(engine.Session{SocketRoot: root})
	assert.NoDirExists(t, root, "an empty root goes")

	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "wake.sock"), nil, 0o600))
	ReleaseSocketRoot(engine.Session{SocketRoot: root})
	assert.FileExists(t, filepath.Join(root, "wake.sock"), "a root still in use stays")

	ReleaseSocketRoot(engine.Session{}) // no root: nothing to do, and no panic
}
