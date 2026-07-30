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

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// captureWarnings redirects clidiag's process-wide warning sink for the test
// and returns an accessor for what the codec wrote there. The read loop warns
// from its own goroutine, hence the locked buffer.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	buf := &lockedBuffer{}
	restore := clidiag.SetSink(buf)
	t.Cleanup(restore)
	return buf.String
}

// waitDone fails the test if the read loop has not finished with the script.
func waitDone(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("read loop never reached EOF")
	}
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

// TestRouteResponse_NullIDErrorIsSurfaced pins U013-F05: JSON-RPC 2.0 mandates
// `"id": null` on an error the peer cannot attribute to a request — a Parse
// error (-32700) or an Invalid Request (-32600), i.e. exactly the cases where
// the peer could not read what WE sent. Unmarshalling JSON null into an int64
// is a documented no-op in encoding/json, so the frame used to route as id 0 —
// an id this codec never allocates, since ids start at 1 — and the peer's
// report was reduced to "dropping response for unknown id 0", with the code
// and message thrown away. The one diagnostic that says our own output was
// unreadable must not be the one we discard.
func TestRouteResponse_NullIDErrorIsSurfaced(t *testing.T) {
	warnings := captureWarnings(t)
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`,
	}, &mockHandler{})
	conn.Start(context.Background())
	waitDone(t, conn)

	got := warnings()
	assert.Contains(t, got, "-32700", "the peer's error code must survive")
	assert.Contains(t, got, "Parse error", "the peer's error message must survive")
	assert.NotContains(t, got, "unknown id 0", "a null id is not id 0")
}

// TestRouteResponse_StringEchoedIDIsMatched pins U013-F15: JSON-RPC 2.0 permits
// a String id, and peers that echo our numeric id back stringified ("1") are
// common enough to matter. routeResponse unmarshalled the member into an int64
// and dropped anything else, so such a response never reached its caller — the
// call then parked until the connection died or its deadline expired, reporting
// a transport failure for a turn the peer had actually answered. We still SEND
// integer ids; this is only about recognising our own id coming back.
func TestRouteResponse_StringEchoedIDIsMatched(t *testing.T) {
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":"1","result":{"stopReason":"end_turn"}}`,
	}, &mockHandler{})

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got struct {
		StopReason string `json:"stopReason"`
	}
	require.NoError(t, await(ctx, &got), "a stringified echo of our own id must still match its caller")
	assert.Equal(t, "end_turn", got.StopReason)
}
