package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// shortSocketPath is a unix socket path under the 108-byte sun_path limit,
// which a nested t.TempDir can exceed.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// The socket wake's whole contract is ONE newline-terminated JSON line in the
// shape claude's cross-session messaging socket reads, carrying the wake text
// as the user message's content. The fake here reads exactly what a receiver
// would.
func TestSocketWake_PostsOneUserMessageLineCarryingTheWakeText(t *testing.T) {
	path := shortSocketPath(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		got <- line
	}()

	require.NoError(t, NewSocketWake(path).Fire(context.Background(), "0123456789abcdef"))

	var line []byte
	select {
	case line = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver never got a line")
	}
	require.NotEmpty(t, line)
	assert.Equal(t, byte('\n'), line[len(line)-1], "the line is newline-terminated")

	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(line, &msg))
	assert.Equal(t, "user", msg.Type)
	assert.Equal(t, "user", msg.Message.Role)
	assert.Equal(t, spool.WakeText("0123456789abcdef"), msg.Message.Content)
}

// The socket sends no acknowledgement, so the only failure the waker can see
// is a refused connection — and it must see it, never swallow it.
func TestSocketWake_ASocketNobodyListensOnIsAnError(t *testing.T) {
	err := NewSocketWake(shortSocketPath(t)).Fire(context.Background(), "0123456789abcdef")
	assert.Error(t, err)
}
