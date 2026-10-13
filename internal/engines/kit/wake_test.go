package kit

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
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

// The socket is an interactive session's alone, in its private root.
func TestSocketWake_PathIsTheInteractiveSessionsOwn(t *testing.T) {
	w := SocketWake{EnvVar: testWakeVar}
	s := engine.Session{Mode: engine.Interactive}
	assert.Empty(t, w.Path(s), "no session-private root, no socket")
	s.Roots.SessionHome.Engine = "/h"
	assert.Equal(t, filepath.Join("/h", "wake.sock"), w.Path(s))
	s.Mode = engine.Structured
	assert.Empty(t, w.Path(s), "a structured session has nobody to wake")
}
