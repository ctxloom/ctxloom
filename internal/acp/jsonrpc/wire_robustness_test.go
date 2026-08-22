package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

// TestRouteResponse_DuplicateResponseDoesNotWedgeTheReadLoop pins that a
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

// TestRouteResponse_NullIDErrorIsSurfaced pins that JSON-RPC 2.0 mandates
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

// TestRouteResponse_StringEchoedIDIsMatched pins that JSON-RPC 2.0 permits
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

// TestRouteResponse_FloatEchoedIDIsMatched pins the same leniency for the
// other spelling a real peer produces. JSON has one number type, so a runtime
// holding request ids as floats echoes the id 1 back as `1.0` — the same id,
// spelled the only way that runtime can spell it. Matching unmarshalled the
// member into an int64 (which rejects 1.0) and otherwise into a string (which
// rejects a bare number), so the response was dropped as "matching no
// outstanding request" and the caller stayed parked until its deadline for a
// turn that had already completed.
func TestRouteResponse_FloatEchoedIDIsMatched(t *testing.T) {
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":1.0,"result":{"stopReason":"end_turn"}}`,
	}, &mockHandler{})

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got struct {
		StopReason string `json:"stopReason"`
	}
	require.NoError(t, await(ctx, &got), "a float-spelled echo of our own id must still match its caller")
	assert.Equal(t, "end_turn", got.StopReason)
}

// TestResponseID_RejectsIDsThatDenoteNoInteger keeps the leniency above from
// widening into matching the wrong caller. A fractional id, a magnitude no
// int64 can hold, and a non-numeric string each denote no id this codec ever
// allocated, and must be reported as unmatched rather than silently truncated
// or wrapped into some other caller's slot.
func TestResponseID_RejectsIDsThatDenoteNoInteger(t *testing.T) {
	for _, raw := range []string{`1.5`, `1e300`, `-1e300`, `"session-a"`, `null`, `{}`, `[1]`} {
		_, ok := responseID(json.RawMessage(raw))
		assert.False(t, ok, "id %s denotes no integer request id", raw)
	}
	for raw, want := range map[string]int64{`1`: 1, `1.0`: 1, `"1"`: 1, `1e0`: 1, `-7`: -7, `"2"`: 2} {
		got, ok := responseID(json.RawMessage(raw))
		if assert.True(t, ok, "id %s denotes an integer request id", raw) {
			assert.Equal(t, want, got, "id %s", raw)
		}
	}
}

// eofAfter is a reader that ends the stream with err on its first Read, used to
// stand in for a transport that annotates its end-of-stream.
type eofAfter struct{ err error }

func (r eofAfter) Read([]byte) (int, error) { return 0, r.err }

// TestClosedErr_WrappedEOFIsAClosedConnection pins that closedErr compared
// the stored read error to io.EOF with !=, so a clean end-of-stream that
// arrives WRAPPED — any reader in the chain that annotates its error, which is
// the idiomatic thing for one to do — was reported to every parked caller as a
// hard transport failure rather than ErrConnClosed. Callers branch on
// ErrConnClosed to tell "the peer hung up" from "the peer broke", so the
// distinction is the whole point of the function.
func TestClosedErr_WrappedEOFIsAClosedConnection(t *testing.T) {
	out := &lockedBuffer{}
	conn := NewConn(eofAfter{err: fmt.Errorf("stdout pipe: %w", io.EOF)}, out, nil, &mockHandler{})

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aerr := await(ctx, nil)
	require.Error(t, aerr)
	assert.ErrorIs(t, aerr, ErrConnClosed, "a wrapped EOF is still a closed connection, not a transport failure")
}

// TestReadLoop_MalformedFrameDoesNotEndTheSession pins that the package
// doc promises "it warns and continues on a malformed frame rather than tearing
// the session down", and for the malformed frames a stream can actually recover
// from that promise was false — ANY decode error ended the read loop, failed
// every parked caller and killed the session. A frame whose members are the
// wrong JSON type is consumed whole by the decoder, so the stream is still in
// sync behind it and continuing is both possible and what the doc says we do.
func TestReadLoop_MalformedFrameDoesNotEndTheSession(t *testing.T) {
	notified := make(chan string, 4)
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":1,"method":5}`,
		`{"jsonrpc":"2.0","method":"session/update"}`,
	}, notifyRecorder(notified))
	conn.Start(context.Background())

	select {
	case m := <-notified:
		assert.Equal(t, "session/update", m)
	case <-time.After(5 * time.Second):
		t.Fatal("the frame after a wrong-typed member was never dispatched: the session was torn down instead")
	}
	waitDone(t, conn)
}

// TestReadLoop_StdoutNoiseIsSteppedOverAndTheSessionSurvives REVERSES the
// contract this suite used to pin, under the name
// TestReadLoop_UnrecoverableFrameEndsTheSession:
// a JSON syntax error ended the session, on the reasoning that
// json.Decoder is left at an undefined byte with no trustworthy frame boundary
// after it. That reasoning was true of the decoder and false of the wire. ACP
// frames messages as newline-delimited JSON, so the next frame boundary is the
// next newline no matter what the bytes before it were — and an engine's
// stdout is a SHARED channel that really does carry non-JSON: a runtime's
// "Debugger attached", an npm notice, a stray console.log from a
// user-configured adapter. Ending an ACP session over one such line meant a
// prompt already in flight died, and every parked caller with it, because a
// tool the user does not control wrote a diagnostic. Noise is stepped over;
// only the transport ending ends the session.
func TestReadLoop_StdoutNoiseIsSteppedOverAndTheSessionSurvives(t *testing.T) {
	notified := make(chan string, 4)
	conn, _ := newScriptedConn([]string{
		`Debugger attached.`,
		`{"jsonrpc":"2.0", this is not JSON at all`,
		``,
		`npm notice New major version of npm available!`,
		`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`,
		`{"jsonrpc":"2.0","method":"session/update"}`,
	}, notifyRecorder(notified))

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got struct {
		StopReason string `json:"stopReason"`
	}
	require.NoError(t, await(ctx, &got), "the turn completed behind the noise: its caller must be answered, not released with an error")
	assert.Equal(t, "end_turn", got.StopReason, "the real result must survive the noise ahead of it")

	select {
	case m := <-notified:
		assert.Equal(t, "session/update", m, "the session must still be serving traffic after the noise")
	case <-time.After(5 * time.Second):
		t.Fatal("no frame after the non-JSON lines was ever dispatched: the session was torn down instead")
	}
	waitDone(t, conn)
}

// TestReadLoop_TransportErrorStillEndsTheSession keeps the OTHER half of the
// reversal honest. Tolerating unreadable LINES must not become tolerating an
// unreadable STREAM: when the transport itself fails there are no more frames
// coming, and a caller parked on a response has to be released rather than
// left hanging until its deadline.
func TestReadLoop_TransportErrorStillEndsTheSession(t *testing.T) {
	conn := NewConn(eofAfter{err: errors.New("read |0: file already closed")}, &lockedBuffer{}, nil, &mockHandler{})

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aerr := await(ctx, nil)
	require.Error(t, aerr, "a parked caller must be released when the transport dies")
	assert.Contains(t, aerr.Error(), "file already closed", "the transport's own failure must reach the caller, not be flattened to a clean hangup")
	waitDone(t, conn)
}

// TestReadLoop_WrongProtocolVersionIsReported pins that the spec makes
// the jsonrpc member MUST-be-exactly-"2.0", and this codec decoded it and then
// never looked at it — so a peer speaking a different protocol was served in
// silence and any resulting confusion had no first clue attached to it. The
// frame is still dispatched: per this package's stated ethos a version
// mismatch is worth saying out loud, not worth dropping a peer's traffic over.
func TestReadLoop_WrongProtocolVersionIsReported(t *testing.T) {
	warnings := captureWarnings(t)
	notified := make(chan string, 4)
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"1.0","method":"session/update"}`,
	}, notifyRecorder(notified))
	conn.Start(context.Background())

	select {
	case m := <-notified:
		assert.Equal(t, "session/update", m, "a version mismatch is reported, not enforced by dropping traffic")
	case <-time.After(5 * time.Second):
		t.Fatal("the frame was never dispatched")
	}
	waitDone(t, conn)
	assert.Contains(t, warnings(), `"1.0"`, "the version the peer actually claimed must be named")
}

// TestReadLoop_CorrectProtocolVersionIsSilent is the other half: the check must
// not turn every conforming frame into a warning.
func TestReadLoop_CorrectProtocolVersionIsSilent(t *testing.T) {
	warnings := captureWarnings(t)
	notified := make(chan string, 4)
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","method":"session/update"}`,
	}, notifyRecorder(notified))
	conn.Start(context.Background())
	<-notified
	waitDone(t, conn)
	assert.Empty(t, warnings(), "a conforming frame must warn about nothing")
}

// TestAwait_TimeoutNamesTheRequest pins that a caller whose RPC ran out of
// time saw a bare "context deadline exceeded" with nothing identifying which
// request died. On a connection that multiplexes session/prompt, session/cancel
// and every fs/* round trip, that error is unattributable — the one thing the
// reader needs is the one thing it did not carry. errors.Is must still see the
// cause, so the identity callers branch on is unchanged.
func TestAwait_TimeoutNamesTheRequest(t *testing.T) {
	pr, pw := io.Pipe() // a peer that is connected and simply never answers
	t.Cleanup(func() { _ = pw.Close() })
	conn := NewConn(pr, &lockedBuffer{}, nil, &mockHandler{})
	conn.Start(context.Background())

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	aerr := await(ctx, nil)
	require.Error(t, aerr)
	assert.ErrorIs(t, aerr, context.DeadlineExceeded, "the cause must stay matchable")
	assert.Contains(t, aerr.Error(), "session/prompt", "the error must name the RPC that died")
}

// TestAwait_ConnectionCloseNamesTheRequest is the other reported shape: a
// caller released because the transport died learned only that "the connection
// closed", never which in-flight request it lost.
func TestAwait_ConnectionCloseNamesTheRequest(t *testing.T) {
	conn, _ := newScriptedConn(nil, &mockHandler{}) // EOF on the first read

	await, err := conn.Go("fs/read_text_file", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aerr := await(ctx, nil)
	require.Error(t, aerr)
	assert.ErrorIs(t, aerr, ErrConnClosed, "the cause must stay matchable")
	assert.Contains(t, aerr.Error(), "fs/read_text_file", "the error must name the RPC that died")
}

// TestAwait_PeerErrorIsNotWrapped guards the limit of the fix above: a peer's
// own JSON-RPC error object is returned verbatim so callers can keep type-
// asserting it to *Error and reading its code. Annotating THAT one would break
// every caller that branches on the peer's code.
func TestAwait_PeerErrorIsNotWrapped(t *testing.T) {
	conn, _ := newScriptedConn([]string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`,
	}, &mockHandler{})

	await, err := conn.Go("session/nope", nil)
	require.NoError(t, err)
	conn.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aerr := await(ctx, nil)
	require.Error(t, aerr)
	rerr, ok := aerr.(*Error)
	require.True(t, ok, "expected a bare *Error, got %T: %v", aerr, aerr)
	assert.Equal(t, CodeMethodNotFound, rerr.Code)
}

// TestConn_NextIDIsAlignmentSafe pins that a bare int64 field reached with
// atomic.AddInt64 must be 64-bit aligned, and Go guarantees that only for the
// first word of an allocated struct on 32-bit platforms. nextID sits behind a
// pointer, two interfaces and a Mutex — offset 28 on a 32-bit layout, which is
// 4-aligned, not 8 — so the add would panic with "unaligned 64-bit atomic
// operation" on the first outbound request. No release target is 32-bit
// (.goreleaser.yml builds amd64 and arm64 only), so nothing shipped can hit
// it; a `go install` on a 32-bit host can. atomic.Int64 embeds the alignment
// guarantee in the type, so the whole class is gone rather than being
// re-derivable from field order — which is exactly what a future field
// reordering would silently change.
func TestConn_NextIDIsAlignmentSafe(t *testing.T) {
	f, ok := reflect.TypeOf(Conn{}).FieldByName("nextID")
	require.True(t, ok, "nextID must still exist")
	assert.Equal(t, reflect.TypeOf(atomic.Int64{}).String(), f.Type.String(),
		"a 64-bit counter reached atomically must carry its own alignment guarantee, not inherit one from where it happens to sit in the struct")
}

// TestGo_AllocatesIdsFromOne characterizes the id allocation the alignment fix
// must leave untouched: ids start at 1 and increment, which is also why id 0
// can never belong to a caller (see the null-id routing above).
func TestGo_AllocatesIdsFromOne(t *testing.T) {
	out := &lockedBuffer{}
	conn := NewConn(strings.NewReader(""), out, nil, &mockHandler{})

	_, err := conn.Go("first", nil)
	require.NoError(t, err)
	_, err = conn.Go("second", nil)
	require.NoError(t, err)

	var ids []int64
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		var m struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []int64{1, 2}, ids)
}

// TestClose_TearsDownThroughTheCloser pins the half of the contract that is true:
// Close unblocks a parked reader and every parked caller EXACTLY when the
// closer it was given ends the stream. That is the contract NewConn's closer
// parameter now states, and this is what it buys.
func TestClose_TearsDownThroughTheCloser(t *testing.T) {
	pr, pw := io.Pipe() // a connected peer that has not spoken
	conn := NewConn(pr, &lockedBuffer{}, CloserFunc(pw.Close), &mockHandler{})
	conn.Start(context.Background())

	await, err := conn.Go("session/prompt", nil)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	waitDone(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.ErrorIs(t, await(ctx, nil), ErrConnClosed, "the parked caller must be released by the teardown")
}

// TestClose_WithoutACloserCannotStopTheReadLoop pins the LIMIT that was once
// reported as undocumented: nothing in the type gives a Conn the power to
// unblock a reader it does not own, so a Conn built with a nil closer reports
// a successful Close and tears down nothing. That is a real property of the
// type, not a bug to be fixed inside it — the caller chose it — but the doc
// used to promise the opposite, so it is pinned here. If this ever starts
// passing, the correction belongs in the doc, not in a deletion of this test.
func TestClose_WithoutACloserCannotStopTheReadLoop(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	conn := NewConn(pr, &lockedBuffer{}, nil, &mockHandler{})
	conn.Start(context.Background())

	require.NoError(t, conn.Close(), "with nothing to close, Close has nothing to report")
	select {
	case <-conn.Done():
		t.Fatal("a Conn with no closer stopped its own read loop: the documented contract is now wrong")
	case <-time.After(250 * time.Millisecond):
	}
}

// TestStart_CtxIsDispatchScopeNotConnectionLifetime pins its subject as
// it actually stands: the ctx handed to Start scopes handler dispatch and
// NOTHING else — cancelling it leaves the read loop parked in Decode, and the
// connection ends only when the peer ends the stream or the owner pulls the
// closer.
//
// This is deliberately a characterization, not an aspiration. Making
// cancellation tear the connection down was built and measured: the jsonrpc
// package went green, and internal/acp's TestChat_CancelDuringTurn — a test
// named for the intent it protects — went red with "agent did not receive
// session/cancel", reproducibly under full-package scheduling and not at
// GOMAXPROCS=1. An ACP client cancelling a turn must put session/cancel on the
// wire before the transport dies; a Conn that closed itself the instant its ctx
// ended raced that notification off the wire. So the lifetime lever stays with
// the owner, who can order the two. If this test is ever made to assert the
// opposite, that ordering is what has to be solved first.
func TestStart_CtxIsDispatchScopeNotConnectionLifetime(t *testing.T) {
	pr, pw := io.Pipe() // a connected peer that never sends and never hangs up
	t.Cleanup(func() { _ = pw.Close() })
	conn := NewConn(pr, &lockedBuffer{}, CloserFunc(pw.Close), &mockHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	conn.Start(ctx)
	cancel()

	select {
	case <-conn.Done():
		t.Fatal("cancelling Start's ctx now ends the connection: the owner-ordered cancel/close sequence in internal/acp has to be reconciled with it")
	case <-time.After(250 * time.Millisecond):
	}
}

// TestStart_LiveCtxLeavesTheConnectionAlone is the other half: an uncancelled
// ctx must not tear anything down, or every session would end at the first
// scheduling hiccup.
func TestStart_LiveCtxLeavesTheConnectionAlone(t *testing.T) {
	notified := make(chan string, 2)
	pr, pw := io.Pipe()
	conn := NewConn(pr, &lockedBuffer{}, CloserFunc(pw.Close), notifyRecorder(notified))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.Start(ctx)

	_, werr := pw.Write([]byte(`{"jsonrpc":"2.0","method":"session/update"}` + "\n"))
	require.NoError(t, werr)
	select {
	case m := <-notified:
		assert.Equal(t, "session/update", m)
	case <-time.After(5 * time.Second):
		t.Fatal("a live ctx must not stop the read loop")
	}
}

// pendingCount reports how many response slots are currently registered,
// without racing the read loop that also touches the map.
func pendingCount(c *Conn) int {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return len(c.pending)
}

// failingWriter stands in for a transport that is already gone.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("transport gone") }

// TestGo_DroppedAwaitLeaksOnlyUntilTheConnectionDies bounds a real leak. Go's
// contract — exactly one await per successful Go, which owns the slot's
// cleanup — is connascence of execution the type genuinely cannot enforce: the
// cleanup lives in the await's own defer, so a caller that drops the closure
// keeps its slot. What that costs is the point. The slot is a map entry and a
// one-deep channel, it can no longer park the read loop (a duplicate response
// into a full slot is dropped, not blocked on), and failPending reclaims every
// abandoned slot when the connection dies. So the leak is bounded by the
// connection, not permanent, and this pins that bound.
func TestGo_DroppedAwaitLeaksOnlyUntilTheConnectionDies(t *testing.T) {
	pr, pw := io.Pipe()
	conn := NewConn(pr, &lockedBuffer{}, CloserFunc(pw.Close), &mockHandler{})

	for range 3 {
		_, err := conn.Go("session/prompt", nil)
		require.NoError(t, err)
	}
	assert.Equal(t, 3, pendingCount(conn), "an un-awaited Go keeps its slot: that is the connascence the contract carries")

	conn.Start(context.Background())
	require.NoError(t, conn.Close())
	waitDone(t, conn)
	assert.Zero(t, pendingCount(conn), "connection death must reclaim every abandoned slot")
}

// TestGo_FailedWriteLeavesNoPendingSlot pins the one part of that cleanup
// the CODEC owns rather than the caller: when Go fails, no await is ever handed
// out, so nothing else could ever release the slot it had already registered.
func TestGo_FailedWriteLeavesNoPendingSlot(t *testing.T) {
	conn := NewConn(strings.NewReader(""), failingWriter{}, nil, &mockHandler{})

	await, err := conn.Go("session/prompt", nil)
	require.Error(t, err, "the write failed, so the request was never sent")
	assert.Nil(t, await)
	assert.Zero(t, pendingCount(conn), "a Go that returns an error must leave no slot behind")
}
