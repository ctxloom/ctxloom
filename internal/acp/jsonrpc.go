package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// This file is a MINIMAL, self-contained JSON-RPC 2.0 peer over newline-delimited
// JSON — the wire ACP speaks over the agent subprocess's stdio.
//
// Why hand-rolled rather than the SDK's connection: the joshgarnett ACP Go SDK's
// jsonrpc layer (golang.org/x/exp/jsonrpc2) hardcodes the LSP-style Content-Length
// HeaderFramer with no override hook, but ACP frames messages as newline-delimited
// JSON. So the SDK's *connection* is wire-incompatible with real ACP agents, while
// its *wire types* (github.com/joshgarnett/agent-client-protocol-go/acp/api,
// stdlib-only, schema-generated) are exactly right. We therefore reuse the SDK's
// api types and supply this ~200-line codec for the framing. See doc.go.
//
// The codec is a full duplex peer: it issues requests (initialize / session/new /
// session/prompt) AND serves inbound requests (session/request_permission,
// fs/read_text_file, fs/write_text_file) and notifications (session/update) from
// the agent. Per the repo's fault-tolerance ethos it warns and continues on a
// malformed frame rather than tearing the session down.

const jsonrpcVersion = "2.0"

// Standard JSON-RPC 2.0 error codes we emit for inbound requests.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// errConnClosed is returned to a caller parked on a response when the underlying
// transport closes (EOF / read error) before the reply arrives.
var errConnClosed = errors.New("acp: connection closed")

// rpcError is a JSON-RPC 2.0 error object. It doubles as a Go error so a failed
// Call surfaces the agent's error verbatim.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return "acp: rpc error " + strconv.Itoa(e.Code) + ": " + e.Message }

// rpcMessage is the single wire frame covering all four JSON-RPC message kinds;
// which fields are set (and omitempty) distinguishes them on decode:
//   - request:      Method + ID (+ Params)
//   - notification: Method, no ID (+ Params)
//   - response:     ID + (Result xor Error), no Method
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcHandler receives inbound (agent→client) traffic. A request must return
// either a result (any JSON-marshalable value, possibly nil) or an *rpcError; a
// notification returns nothing. Both run on the read-loop goroutine, so a handler
// must not block indefinitely — the session driver's session/update handler only
// blocks on a ctx-aware channel send.
type rpcHandler interface {
	handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *rpcError)
	handleNotification(ctx context.Context, method string, params json.RawMessage)
}

// rpcConn is a bidirectional JSON-RPC 2.0 peer bound to one reader/writer pair
// (the agent subprocess's stdout/stdin). It is safe for concurrent Call/Notify.
type rpcConn struct {
	dec    *json.Decoder
	w      io.Writer
	closer io.Closer

	writeMu sync.Mutex // serializes frame writes (Call, Notify, and inbound responses share the writer)
	nextID  int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	handler rpcHandler

	done      chan struct{} // closed when the read loop exits
	closeOnce sync.Once
	readErr   atomic.Pointer[error]
}

// newRPCConn binds a peer to a reader/writer (and optional closer for teardown)
// and starts its read loop. handler receives inbound requests and notifications.
func newRPCConn(ctx context.Context, r io.Reader, w io.Writer, closer io.Closer, handler rpcHandler) *rpcConn {
	c := &rpcConn{
		dec:     json.NewDecoder(r),
		w:       w,
		closer:  closer,
		handler: handler,
		pending: make(map[int64]chan rpcMessage),
		done:    make(chan struct{}),
	}
	go c.readLoop(ctx)
	return c
}

// call issues a request and blocks until the matching response arrives, ctx is
// cancelled, or the connection closes. On success it unmarshals the result into
// result (when non-nil).
func (c *rpcConn) call(ctx context.Context, method string, params, result any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMessage, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.writeFrame(rpcMessage{Method: method, ID: json.RawMessage(strconv.FormatInt(id, 10)), Params: mustParams(method, params)}); err != nil {
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	}
}

// notify sends a notification (no response expected).
func (c *rpcConn) notify(method string, params any) error {
	return c.writeFrame(rpcMessage{Method: method, Params: mustParams(method, params)})
}

// close tears down the transport and unblocks any parked reader/caller.
func (c *rpcConn) close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.closer != nil {
			err = c.closer.Close()
		}
	})
	return err
}

// readLoop decodes frames until EOF/error, routing each to a pending caller
// (response), a handler (request/notification), or a warning (garbage). A decode
// error ends the session — it fails every parked caller and closes done.
func (c *rpcConn) readLoop(ctx context.Context) {
	defer close(c.done)
	for {
		var m rpcMessage
		if err := c.dec.Decode(&m); err != nil {
			c.readErr.Store(&err)
			c.failPending()
			return
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.serveRequest(ctx, m)
		case m.Method != "":
			c.handler.handleNotification(ctx, m.Method, m.Params)
		case len(m.ID) > 0:
			c.routeResponse(m)
		default:
			warnf("acp: dropping unrecognized JSON-RPC frame (no method, no id)")
		}
	}
}

// serveRequest dispatches an inbound request to the handler and writes back a
// response echoing the request id. A success with no value still carries a JSON
// null result so the frame is a valid JSON-RPC response.
func (c *rpcConn) serveRequest(ctx context.Context, m rpcMessage) {
	result, rerr := c.handler.handleRequest(ctx, m.Method, m.Params)
	resp := rpcMessage{ID: m.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = marshalResult(result)
	}
	if err := c.writeFrame(resp); err != nil {
		warnf("acp: failed to write response to %q: %v", m.Method, err)
	}
}

// routeResponse hands a response frame to the caller waiting on its id.
func (c *rpcConn) routeResponse(m rpcMessage) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		warnf("acp: dropping response with non-integer id %s", m.ID)
		return
	}
	c.pendingMu.Lock()
	ch := c.pending[id]
	c.pendingMu.Unlock()
	if ch == nil {
		warnf("acp: dropping response for unknown id %d", id)
		return
	}
	ch <- m
}

// failPending unblocks every parked caller when the connection dies.
func (c *rpcConn) failPending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		close(ch) // a closed channel yields the zero rpcMessage; call() then returns closedErr via <-c.done
		delete(c.pending, id)
	}
}

func (c *rpcConn) closedErr() error {
	if p := c.readErr.Load(); p != nil && *p != nil && *p != io.EOF {
		return *p
	}
	return errConnClosed
}

// writeFrame marshals one frame and writes it followed by a newline (ACP framing).
func (c *rpcConn) writeFrame(m rpcMessage) error {
	m.JSONRPC = jsonrpcVersion
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(data)
	return err
}

// mustParams marshals a params value to raw JSON, warning (and sending no params)
// rather than failing the whole frame if the value can't be marshaled.
func mustParams(method string, params any) json.RawMessage {
	if params == nil {
		return nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		warnf("acp: failed to marshal params for %q: %v", method, err)
		return nil
	}
	return data
}

// marshalResult renders a handler's return value as a response result, defaulting
// to JSON null (a valid, content-less success) when nil or unmarshalable.
func marshalResult(result any) json.RawMessage {
	if result == nil {
		return json.RawMessage("null")
	}
	data, err := json.Marshal(result)
	if err != nil {
		warnf("acp: failed to marshal result: %v", err)
		return json.RawMessage("null")
	}
	return data
}
