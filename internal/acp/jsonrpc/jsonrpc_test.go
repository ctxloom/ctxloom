package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

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
	conn := NewConn(context.Background(), s2cR, c2sW, closer, handler)
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
