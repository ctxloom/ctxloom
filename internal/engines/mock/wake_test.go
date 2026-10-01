package mock

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

func TestWake_TheMockDeclaresItsOwnSocketWake(t *testing.T) {
	spec, ok := Mock{}.Wake().Get()
	require.True(t, ok)
	assert.IsType(t, socketWake{}, spec)
}

func TestSocketWake_BindRefusesWithoutTheSocketVariable(t *testing.T) {
	for _, env := range []map[string]string{{}, {EnvWakeSocket: ""}} {
		_, err := socketWake{}.Bind(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		require.ErrorIs(t, err, engine.ErrWakeUnbound)
		assert.Contains(t, err.Error(), EnvWakeSocket)
	}
}

// The mock's wake is one line, the wake text, on its own socket: the mock
// runtime takes it exactly as a typed line.
func TestSocketWake_FirePostsTheWakeTextAsOneLine(t *testing.T) {
	dir, err := os.MkdirTemp("", "mw")
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
	w, err := socketWake{}.Bind(context.Background(), func(k string) (string, bool) { return path, k == EnvWakeSocket })
	require.NoError(t, err)

	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	assert.Equal(t, []string{engine.WakeText("0123456789abcdef")}, testsupport.Await(t, 10*time.Second, (<-chan []string)(got), "nothing was posted"))
}

func TestSocketWake_AnUndeliverableWakeFails(t *testing.T) {
	w, err := socketWake{}.Bind(context.Background(), func(string) (string, bool) { return filepath.Join(t.TempDir(), "absent.sock"), true })
	require.NoError(t, err)
	require.Error(t, w.Fire(context.Background(), "0123456789abcdef"))
}
