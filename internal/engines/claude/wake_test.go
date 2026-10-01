package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// testToken stands in for CLAUDE_CODE_MESSAGING_TOKEN: distinctive enough
// that finding it in any error or log is unambiguous.
const testToken = "tok-5f1d0c9e-never-print-me"

// envOf is an engine.WakeEnv over a fixed map.
func envOf(m map[string]string) engine.WakeEnv {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// listenMessaging stands up a unix listener where claude's messaging socket
// would be and returns its path and a channel carrying the lines of the ONE
// connection it accepts, read to EOF — the poster closing is the end of a
// post, so a receive after Fire returns is synchronised, not polled.
func listenMessaging(t *testing.T) (string, <-chan []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "m.sock")
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
	return path, got
}

func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m), "line %q", line)
	return m
}

func TestWake_ClaudeDeclaresItsMessagingWake(t *testing.T) {
	w := Claude{}.Wake()
	assert.True(t, w.Decided())
	spec, ok := w.Get()
	require.True(t, ok, "claude provides its wake: %s", w.AbsentReason())
	assert.IsType(t, messagingWake{}, spec)
}

// Without the socket claude exports to its own children there is nothing to
// post to: the bind refuses, naming the variable, and never guesses a path.
func TestMessagingWake_BindRefusesWithoutTheSocketVariable(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{envMessagingSocket: ""},
		{envMessagingToken: testToken},
	} {
		_, err := messagingWake{}.Bind(context.Background(), envOf(env))
		require.ErrorIs(t, err, engine.ErrWakeUnbound)
		assert.Contains(t, err.Error(), envMessagingSocket)
		assert.NotContains(t, err.Error(), testToken)
	}
}

// The post is the auth line first (whenever claude exported a token), then
// ONE user line carrying the wake text, then close. Nothing is read back.
func TestMessagingWake_FirePostsAuthThenTheWakeLine(t *testing.T) {
	path, got := listenMessaging(t)
	w, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: path, envMessagingToken: testToken}))
	require.NoError(t, err)

	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	lines := <-got
	require.Len(t, lines, 2)
	assert.Equal(t, map[string]any{"type": "auth", "token": testToken}, decodeLine(t, lines[0]))
	assert.Equal(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": engine.WakeText("0123456789abcdef")},
	}, decodeLine(t, lines[1]))
}

func TestMessagingWake_FireWithoutATokenPostsOnlyTheWakeLine(t *testing.T) {
	path, got := listenMessaging(t)
	w, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: path}))
	require.NoError(t, err)

	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	lines := <-got
	require.Len(t, lines, 1)
	assert.Equal(t, "user", decodeLine(t, lines[0])["type"])
}

// A post that cannot be delivered is an error the caller sees — a silent
// wake is indistinguishable from a delivered one — and the error carries the
// endpoint but never the token, which is a full permission-parity bypass on
// Windows.
func TestMessagingWake_AnUndeliverablePostFailsWithoutTheToken(t *testing.T) {
	dir, err := os.MkdirTemp("", "cw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	gone := filepath.Join(dir, "absent.sock")
	w, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: gone, envMessagingToken: testToken}))
	require.NoError(t, err)

	err = w.Fire(context.Background(), "0123456789abcdef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), gone)
	assert.NotContains(t, err.Error(), testToken)
}

// The bound wake's printed form is what lands in a log line when a caller
// formats it with %v: it must not be the token.
func TestMessagingWake_TheBoundWakeNeverFormatsTheToken(t *testing.T) {
	w, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: "/x", envMessagingToken: testToken}))
	require.NoError(t, err)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assert.NotContains(t, fmt.Sprintf(verb, w), testToken, verb)
	}
}
