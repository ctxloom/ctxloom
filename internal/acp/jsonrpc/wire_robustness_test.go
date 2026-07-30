package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer captures the frames a Conn writes without racing the read loop,
// which writes responses from its own goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newScriptedConn binds a Conn to a fixed script of raw inbound frames. The
// read loop is deliberately NOT started: a test that must register a pending
// slot before the first frame is decoded would otherwise race the loop. Call
// Start when the fixture is ready. A fixed reader also means EOF is reached
// exactly when the script runs out, so "did the read loop survive frame N" is
// answerable by waiting on Done rather than by timing.
func newScriptedConn(frames []string, handler Handler) (*Conn, *lockedBuffer) {
	out := &lockedBuffer{}
	script := ""
	if len(frames) > 0 {
		script = strings.Join(frames, "\n") + "\n"
	}
	return NewConn(strings.NewReader(script), out, nil, handler), out
}

// notifyRecorder is a Handler that records the inbound notifications it sees.
func notifyRecorder(ch chan<- string) *mockHandler {
	return &mockHandler{onNotify: func(_ context.Context, method string, _ json.RawMessage) {
		ch <- method
	}}
}

// TestRouteResponse_DuplicateResponseDoesNotWedgeTheReadLoop pins U013-F04: a
// peer that sends two responses for one id must not be able to park the read
// loop forever. routeResponse ran `ch <- m` on the read-loop goroutine against
// a cap-1 buffer, so the second response blocked until someone drained the
// slot — and the caller drains it exactly once. With the slot already full and
// no second receive coming, the send never completes: no further frame is ever
// decoded, no notification is dispatched, and Done never closes. Duplicate
// responses for one id are forbidden by JSON-RPC 2.0, so the first answer wins
// and the extra is dropped with a warning.
func TestRouteResponse_DuplicateResponseDoesNotWedgeTheReadLoop(t *testing.T) {
	notified := make(chan string, 4)
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","method":"session/update"}`,
	}, notifyRecorder(notified))

	// Register pending id 1 and never await it: that is exactly the state a
	// caller occupies between its response landing in the slot and its await
	// draining it, held still so the outcome is deterministic.
	_, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)

	conn.Start(context.Background())

	select {
	case m := <-notified:
		assert.Equal(t, "session/update", m)
	case <-time.After(5 * time.Second):
		t.Fatal("read loop wedged: the notification following a duplicate response was never dispatched")
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("read loop never reached EOF after a duplicate response")
	}
}
