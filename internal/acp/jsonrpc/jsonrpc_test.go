package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHandler is an Handler with pluggable behavior for the codec tests.
type mockHandler struct {
	onRequest func(ctx context.Context, method string, params json.RawMessage) (any, *Error)
	onNotify  func(ctx context.Context, method string, params json.RawMessage)
}

func (m *mockHandler) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *Error)) {
	if m.onRequest != nil {
		reply(m.onRequest(ctx, method, params))
		return
	}
	reply(nil, &Error{Code: CodeMethodNotFound, Message: "no handler"})
}

func (m *mockHandler) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	if m.onNotify != nil {
		m.onNotify(ctx, method, params)
	}
}

// newTestConn wires an Conn to an in-memory "server" side over two pipes,
// returning the conn plus the server's frame reader and a frame writer.
func newTestConn(t *testing.T, handler Handler) (*Conn, *json.Decoder, func(rpcMessage)) {
	t.Helper()
	c2sR, c2sW := io.Pipe() // client → server
	s2cR, s2cW := io.Pipe() // server → client

	closer := CloserFunc(func() error {
		_ = c2sW.Close()
		_ = s2cR.Close()
		return nil
	})
	conn := NewConn(s2cR, c2sW, closer, handler)
	conn.Start(context.Background())
	t.Cleanup(func() { _ = conn.Close() })

	serverDec := json.NewDecoder(c2sR)
	serverWrite := func(m rpcMessage) {
		m.JSONRPC = jsonrpcVersion
		data, err := json.Marshal(m)
		require.NoError(t, err)
		data = append(data, '\n')
		_, werr := s2cW.Write(data)
		require.NoError(t, werr)
	}
	return conn, serverDec, serverWrite
}

// TestWriteFrame_NewlineDelimited pins the load-bearing wire fact: frames are
// newline-delimited JSON (NOT LSP Content-Length), one JSON object per line.
func TestWriteFrame_NewlineDelimited(t *testing.T) {
	var buf bytes.Buffer
	c := &Conn{w: &buf}
	require.NoError(t, c.writeFrame(rpcMessage{Method: "ping"}))
	require.NoError(t, c.writeFrame(rpcMessage{Method: "pong"}))

	assert.NotContains(t, buf.String(), "Content-Length")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"ping"}`, lines[0])
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"pong"}`, lines[1])
}

// TestCall_RequestResponse pins the outbound request → matched response path.
func TestCall_RequestResponse(t *testing.T) {
	conn, serverDec, serverWrite := newTestConn(t, &mockHandler{})

	type result struct {
		OK bool `json:"ok"`
	}
	var res result
	done := make(chan error, 1)
	go func() {
		done <- conn.Call(context.Background(), "initialize", map[string]any{"protocolVersion": 1}, &res)
	}()

	var req rpcMessage
	require.NoError(t, serverDec.Decode(&req))
	assert.Equal(t, jsonrpcVersion, req.JSONRPC)
	assert.Equal(t, "initialize", req.Method)
	require.NotEmpty(t, req.ID)
	assert.JSONEq(t, `{"protocolVersion":1}`, string(req.Params))

	serverWrite(rpcMessage{ID: req.ID, Result: json.RawMessage(`{"ok":true}`)})

	require.NoError(t, <-done)
	assert.True(t, res.OK)
}

// TestCall_ErrorResponse pins that a JSON-RPC error is surfaced verbatim.
func TestCall_ErrorResponse(t *testing.T) {
	conn, serverDec, serverWrite := newTestConn(t, &mockHandler{})

	done := make(chan error, 1)
	go func() { done <- conn.Call(context.Background(), "session/new", map[string]any{}, nil) }()

	var req rpcMessage
	require.NoError(t, serverDec.Decode(&req))
	serverWrite(rpcMessage{ID: req.ID, Error: &Error{Code: -32000, Message: "auth required"}})

	err := <-done
	var rerr *Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, -32000, rerr.Code)
	assert.Contains(t, rerr.Error(), "auth required")
}

// TestErrorString_IncludesData pins that the error's data payload rides the
// Go error string. Agents (the ACP TS SDK in particular) reply to any thrown
// handler exception with a generic "Internal error" message and the real
// cause in data — the claude-code-acp session/new failure that motivated
// this printed only "acp: rpc error -32603: Internal error" while the root
// cause sat in data.details.
func TestErrorString_IncludesData(t *testing.T) {
	withData := &Error{Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"Query closed before response received"}`)}
	assert.Equal(t,
		`acp: rpc error -32603: Internal error ({"details":"Query closed before response received"})`,
		withData.Error())

	assert.Equal(t, "acp: rpc error -32000: auth required",
		(&Error{Code: -32000, Message: "auth required"}).Error(), "no data → unchanged")
	assert.Equal(t, "acp: rpc error -32603: Internal error",
		(&Error{Code: -32603, Message: "Internal error", Data: json.RawMessage(`null`)}).Error(), "JSON null data adds nothing")
}

// TestNotify pins that a notification carries a method and no id.
func TestNotify(t *testing.T) {
	conn, serverDec, _ := newTestConn(t, &mockHandler{})

	// notify writes to an unbuffered pipe, so read concurrently to unblock it.
	notified := make(chan error, 1)
	go func() { notified <- conn.Notify("session/cancel", map[string]any{"sessionId": "s1"}) }()

	var m rpcMessage
	require.NoError(t, serverDec.Decode(&m))
	require.NoError(t, <-notified)
	assert.Equal(t, "session/cancel", m.Method)
	assert.Empty(t, m.ID, "a notification must not carry an id")
	assert.JSONEq(t, `{"sessionId":"s1"}`, string(m.Params))
}

// TestServeInboundRequest pins that an agent→client request is dispatched to the
// handler and its result is written back echoing the request id.
func TestServeInboundRequest(t *testing.T) {
	handler := &mockHandler{
		onRequest: func(_ context.Context, method string, params json.RawMessage) (any, *Error) {
			assert.Equal(t, "session/request_permission", method)
			assert.JSONEq(t, `{"options":[]}`, string(params))
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		},
	}
	_, serverDec, serverWrite := newTestConn(t, handler)

	serverWrite(rpcMessage{ID: json.RawMessage(`42`), Method: "session/request_permission", Params: json.RawMessage(`{"options":[]}`)})

	var resp rpcMessage
	require.NoError(t, serverDec.Decode(&resp))
	assert.Equal(t, "42", string(resp.ID))
	assert.Nil(t, resp.Error)
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, string(resp.Result))
}

// TestServeInboundRequest_UnknownMethod pins the fault-tolerant rejection of an
// unsupported callback (e.g. the declined terminal/*) as method-not-found.
func TestServeInboundRequest_UnknownMethod(t *testing.T) {
	_, serverDec, serverWrite := newTestConn(t, &mockHandler{})

	serverWrite(rpcMessage{ID: json.RawMessage(`7`), Method: "terminal/create", Params: json.RawMessage(`{}`)})

	var resp rpcMessage
	require.NoError(t, serverDec.Decode(&resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeMethodNotFound, resp.Error.Code)
}

// TestServeRequest_HandlerPanicRecovered pins U013-F01: a panic inside
// HandleRequest used to run unrecovered on the read-loop goroutine, killing
// the whole process. It must instead answer the panicking request with a
// -32603 error (the reference ACP TS SDK's own contract — this package's doc
// says so) AND leave the read loop alive to serve the next request.
func TestServeRequest_HandlerPanicRecovered(t *testing.T) {
	calls := 0
	_, serverDec, serverWrite := newTestConn(t, &mockHandler{
		onRequest: func(_ context.Context, method string, _ json.RawMessage) (any, *Error) {
			calls++
			if calls == 1 {
				panic("boom: simulated handler panic")
			}
			return map[string]any{"ok": true}, nil
		},
	})

	serverWrite(rpcMessage{ID: json.RawMessage(`1`), Method: "fs/read_text_file"})
	var resp1 rpcMessage
	require.NoError(t, serverDec.Decode(&resp1))
	require.NotNil(t, resp1.Error, "a panicking handler must still produce an error reply, not silence and not a crash")
	assert.Equal(t, CodeInternalError, resp1.Error.Code)
	assert.Contains(t, resp1.Error.Message, "boom")

	// The read loop must have survived the panic: prove it by serving a second,
	// ordinary request on the SAME connection.
	serverWrite(rpcMessage{ID: json.RawMessage(`2`), Method: "fs/read_text_file"})
	var resp2 rpcMessage
	require.NoError(t, serverDec.Decode(&resp2))
	require.Nil(t, resp2.Error, "read loop must keep serving requests after a handler panic")
	assert.JSONEq(t, `{"ok":true}`, string(resp2.Result))
}

// TestServeNotification_HandlerPanicRecovered is TestServeRequest_HandlerPanicRecovered's
// twin for the notification dispatch path (readLoop's other inline call,
// HandleNotification — U013-F01 named both).
func TestServeNotification_HandlerPanicRecovered(t *testing.T) {
	calls := 0
	notified := make(chan string, 1)
	_, _, serverWrite := newTestConn(t, &mockHandler{
		onNotify: func(_ context.Context, method string, _ json.RawMessage) {
			calls++
			if calls == 1 {
				panic("boom: simulated notification panic")
			}
			notified <- method
		},
	})

	serverWrite(rpcMessage{Method: "session/cancel"})
	serverWrite(rpcMessage{Method: "session/cancel"})

	select {
	case m := <-notified:
		assert.Equal(t, "session/cancel", m)
	case <-time.After(5 * time.Second):
		t.Fatal("read loop did not survive a notification handler panic")
	}
}
