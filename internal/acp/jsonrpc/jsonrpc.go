// Package jsonrpc is a MINIMAL, self-contained JSON-RPC 2.0 peer over
// newline-delimited JSON — the wire the Agent Client Protocol speaks over
// stdio. It serves BOTH halves of ctxloom's ACP story: the client driver
// (internal/acp, spawning `<agent> acp`) and the agent server (internal/
// acpagent, `ctxloom acp`).
//
// Why hand-rolled rather than the SDK's connection: the joshgarnett ACP Go
// SDK's jsonrpc layer (golang.org/x/exp/jsonrpc2) hardcodes the LSP-style
// Content-Length HeaderFramer with no override hook, but ACP frames messages
// as newline-delimited JSON. So the SDK's *connection* is wire-incompatible
// with real ACP agents, while its *wire types* (…/acp/api, stdlib-only,
// schema-generated) are exactly right. We reuse the SDK's api types and supply
// this codec for the framing. See internal/acp/doc.go.
//
// The codec is a full duplex peer: it issues requests AND serves inbound
// requests/notifications. Inbound requests are answered through a REPLY
// CALLBACK so a handler may respond asynchronously — load-bearing for the
// agent server, whose session/prompt runs a whole engine turn and must not
// block the read loop (a blocked read loop could never see session/cancel).
// Per the repo's fault-tolerance ethos it warns and continues on a malformed
// frame rather than tearing the session down.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

const jsonrpcVersion = "2.0"

// Standard JSON-RPC 2.0 error codes emitted for inbound requests.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ErrConnClosed is returned to a caller parked on a response when the
// underlying transport closes (EOF / read error) before the reply arrives.
var ErrConnClosed = errors.New("acp: connection closed")

// Error is a JSON-RPC 2.0 error object. It doubles as a Go error so a failed
// Call surfaces the peer's error verbatim.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error renders the peer's error INCLUDING its data payload: agents commonly
// reply with a generic message ("Internal error") and put the actual cause in
// data (the ACP TS SDK wraps any thrown handler exception exactly that way),
// so dropping data here would reduce a root cause to an opaque -32603 — the
// child-obituary blindness the agentcoord Wave B1 debugging hit.
func (e *Error) Error() string {
	s := "acp: rpc error " + strconv.Itoa(e.Code) + ": " + e.Message
	if len(e.Data) > 0 && string(e.Data) != "null" {
		s += " (" + string(e.Data) + ")"
	}
	return s
}

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
	Error   *Error          `json:"error,omitempty"`
}

// Handler receives inbound traffic. HandleRequest must call reply EXACTLY once
// — inline for quick answers, or from another goroutine for long-running work
// (an engine turn); the reply writes the response frame. Handlers run on the
// read-loop goroutine, so anything slow must move off it before blocking.
type Handler interface {
	HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(result any, rerr *Error))
	HandleNotification(ctx context.Context, method string, params json.RawMessage)
}

// Conn is a bidirectional JSON-RPC 2.0 peer bound to one reader/writer pair.
// It is safe for concurrent Call/Notify.
type Conn struct {
	dec    *json.Decoder
	w      io.Writer
	closer io.Closer

	writeMu sync.Mutex // serializes frame writes (Call, Notify, and inbound responses share the writer)
	nextID  int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	handler Handler

	done      chan struct{} // closed when the read loop exits
	closeOnce sync.Once
	readErr   atomic.Pointer[error]
}

// NewConn binds a peer to a reader/writer (and optional closer for teardown).
// handler receives inbound requests and notifications. The read loop does
// NOT start until Start is called (see its doc) — call Start once
// construction is complete.
func NewConn(r io.Reader, w io.Writer, closer io.Closer, handler Handler) *Conn {
	return &Conn{
		dec:     json.NewDecoder(r),
		w:       w,
		closer:  closer,
		handler: handler,
		pending: make(map[int64]chan rpcMessage),
		done:    make(chan struct{}),
	}
}

// Start begins the read loop, dispatching inbound frames to handler. It must
// be called exactly once, after NewConn.
//
// Splitting this from NewConn closes a real data race a caller can otherwise
// fall into: a Handler that stores the *Conn NewConn returns back onto
// itself (so its own methods can call Notify/Call — acpagent's Server does
// exactly this: `s.conn = jsonrpc.NewConn(r, w, nil, s)`) has no
// guarantee that assignment is visible to the read loop, because — before
// this split — the read loop's goroutine was spawned INSIDE NewConn, before
// NewConn had even returned to the caller, let alone before the caller's
// assignment executed. The read loop (and anything IT spawns, e.g. a
// request handler goroutine) is a goroutine distinct from the caller's, so
// Go's memory model gives it no happens-before edge to a write that occurs
// strictly after its own creation — any Handler method reading that
// self-stored field is racing the store, however astronomically unlikely a
// same-process interleaving makes it look. It surfaced for real under
// heavy scheduling contention (many concurrent test processes), confirmed
// with -race: `s.conn` written by Serve() racing a read in
// (*Server).emitUpdate, reached from a request the read loop dispatched
// before Serve()'s assignment had committed.
//
// Calling Start only after the handler has finished publishing whatever
// state it needs (as acpagent.Serve now does: assign s.conn, THEN Start)
// makes Start's own goroutine creation the happens-before edge instead —
// the read loop, and everything it dispatches, is guaranteed to see
// everything the caller did up to and including the Start call.
func (c *Conn) Start(ctx context.Context) {
	go c.readLoop(ctx)
}

// Call issues a request and blocks until the matching response arrives, ctx is
// cancelled, or the connection closes. On success it unmarshals the result into
// result (when non-nil).
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	await, err := c.Go(method, params)
	if err != nil {
		return err
	}
	return await(ctx, result)
}

// Go issues a request and returns once the frame is WRITTEN — so a caller can
// order later frames (e.g. a session/cancel notification) strictly after this
// request — plus an await that blocks for the response. Exactly one await call
// must follow a successful Go (it owns the pending slot's cleanup).
func (c *Conn) Go(method string, params any) (func(ctx context.Context, result any) error, error) {
	// A frame with no method is garbage the peer must drop (U013-F17): refuse
	// it here rather than emitting {"jsonrpc":"2.0"} and reporting success.
	if method == "" {
		return nil, errors.New("acp: refusing to send a request with no method")
	}
	rawParams, perr := marshalParams(method, params)
	if perr != nil {
		return nil, perr
	}
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMessage, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.writeFrame(rpcMessage{Method: method, ID: json.RawMessage(strconv.FormatInt(id, 10)), Params: rawParams}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, err
	}

	await := func(ctx context.Context, result any) error {
		defer func() {
			c.pendingMu.Lock()
			delete(c.pending, id)
			c.pendingMu.Unlock()
		}()
		select {
		case resp, ok := <-ch:
			if !ok {
				return c.closedErr() // failPending closed the slot (connection died)
			}
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
	return await, nil
}

// Notify sends a notification (no response expected).
func (c *Conn) Notify(method string, params any) error {
	if method == "" {
		return errors.New("acp: refusing to send a notification with no method")
	}
	rawParams, err := marshalParams(method, params)
	if err != nil {
		return err
	}
	return c.writeFrame(rpcMessage{Method: method, Params: rawParams})
}

// Close tears down the transport and unblocks any parked reader/caller.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.closer != nil {
			err = c.closer.Close()
		}
	})
	return err
}

// Done is closed when the read loop exits (EOF, read error, or Close).
func (c *Conn) Done() <-chan struct{} { return c.done }

// readLoop decodes frames until EOF/error, routing each to a pending caller
// (response), the handler (request/notification), or a warning (garbage). A
// decode error ends the session — it fails every parked caller and closes done.
func (c *Conn) readLoop(ctx context.Context) {
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
			c.serveNotification(ctx, m)
		case len(m.ID) > 0:
			c.routeResponse(m)
		default:
			warnf("acp: dropping unrecognized JSON-RPC frame (no method, no id)")
		}
	}
}

// serveNotification dispatches an inbound notification with the same panic
// recovery serveRequest gives requests (U013-F01). A notification has no
// reply slot to carry an error back on, so a panic here can only be warned,
// not answered — but warning and continuing still beats taking the whole
// read loop (and the process) down over one malformed notification.
func (c *Conn) serveNotification(ctx context.Context, m rpcMessage) {
	defer func() {
		if r := recover(); r != nil {
			warnf("acp: handler panic in notification %q: %v", m.Method, r)
		}
	}()
	c.handler.HandleNotification(ctx, m.Method, m.Params)
}

// serveRequest dispatches an inbound request to the handler with a once-guarded
// reply callback that writes the response echoing the request id. A success
// with no value still carries a JSON null result so the frame is a valid
// JSON-RPC response. The handler owns WHEN reply runs (sync or async).
func (c *Conn) serveRequest(ctx context.Context, m rpcMessage) {
	var once sync.Once
	method, id := m.Method, m.ID
	reply := func(result any, rerr *Error) {
		once.Do(func() {
			resp := rpcMessage{ID: id}
			if rerr != nil {
				resp.Error = rerr
			} else if raw, merr := marshalResult(result); merr != nil {
				warnf("acp: result for %q could not be encoded; answering with an error instead of a null success: %s", method, merr.Message)
				resp.Error = merr
			} else {
				resp.Result = raw
			}
			if err := c.writeFrame(resp); err != nil {
				warnf("acp: failed to write response to %q: %v", method, err)
			}
		})
	}
	// U013-F01: HandleRequest runs INLINE on this read-loop goroutine. Without a
	// recover, any panic inside a handler — a slice bounds error deep in a fs/*
	// handler, say — is unrecovered on this goroutine and terminates the whole
	// ctxloom process, mid-conversation, with no reply ever written. The
	// reference ACP TypeScript SDK converts a thrown handler exception into a
	// -32603 reply (this package's own doc says so); this recover restores that
	// parity. reply is once-guarded, so a handler that already replied before
	// panicking is unaffected — this only fires the error branch when nothing
	// else has.
	defer func() {
		if r := recover(); r != nil {
			warnf("acp: handler panic in %q: %v", method, r)
			reply(nil, &Error{Code: CodeInternalError, Message: fmt.Sprintf("acp: handler panic in %q: %v", method, r)})
		}
	}()
	c.handler.HandleRequest(ctx, method, m.Params, reply)
}

// routeResponse hands a response frame to the caller waiting on its id.
func (c *Conn) routeResponse(m rpcMessage) {
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
func (c *Conn) failPending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		close(ch) // the await detects the closed slot and returns closedErr
		delete(c.pending, id)
	}
}

func (c *Conn) closedErr() error {
	if p := c.readErr.Load(); p != nil && *p != nil && *p != io.EOF {
		return *p
	}
	return ErrConnClosed
}

// writeFrame marshals one frame and writes it followed by a newline (ACP framing).
func (c *Conn) writeFrame(m rpcMessage) error {
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

// marshalParams marshals a params value to raw JSON. A marshal failure is an
// ERROR, not a stripped frame: this used to warn and return nil, and the
// caller sent the request/notification anyway — with its entire payload
// silently removed, which the peer answers as best it can (U013-F03).
func marshalParams(method string, params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("acp: refusing to send %q with its params stripped: %w", method, err)
	}
	return data, nil
}

// marshalResult renders a handler's return value as a response result. A nil
// result is JSON null — a valid, content-less success. An UNMARSHALABLE result
// is an internal error, not a null success: telling the peer the request
// succeeded while handing it zero payload is the house lie in wire form
// (U013-F02).
func marshalResult(result any) (json.RawMessage, *Error) {
	if result == nil {
		return json.RawMessage("null"), nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, &Error{Code: CodeInternalError, Message: "result could not be encoded: " + err.Error()}
	}
	return data, nil
}

// warnf routes codec diagnostics through ctxloom's standard warning path.
func warnf(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) }

// CloserFunc adapts a teardown func to io.Closer for NewConn.
type CloserFunc func() error

// Close runs the adapted teardown (nil-safe).
func (f CloserFunc) Close() error {
	if f == nil {
		return nil
	}
	return f()
}
