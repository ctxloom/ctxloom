// Package jsonrpc is a MINIMAL, self-contained JSON-RPC 2.0 peer over
// newline-delimited JSON — the wire the Agent Client Protocol speaks over
// stdio. It serves BOTH halves of ctxloom's ACP story: the client driver
// (internal/acp, spawning `<agent> acp`) and the agent server (internal/
// acpagent, `ctxloom acp`).
//
// This codec is scheduled for retirement in favour of acp.Connection from
// github.com/coder/acp-go-sdk — already a direct dependency for the wire
// types, and already newline-framed. It still exists only because adopting
// that Connection needs accommodations this package does not yet have
// equivalents for. See internal/acp/doc.go.
//
// The codec is a full duplex peer: it issues requests AND serves inbound
// requests/notifications. Inbound requests are answered through a REPLY
// CALLBACK so a handler may respond asynchronously — load-bearing for the
// agent server, whose session/prompt runs a whole engine turn and must not
// block the read loop (a blocked read loop could never see session/cancel).
// Per the repo's fault-tolerance ethos it warns and continues on ANY line it
// cannot read as a JSON-RPC message — malformed JSON, a non-JSON line, or a
// frame whose members are the wrong JSON type — rather than tearing the
// session down. Framing is what makes that safe: on a newline-delimited wire
// the next frame boundary is the next '\n', so a line the parser rejects costs
// exactly that line. A stray "Debugger attached", a console.log from a
// user-configured adapter, or an npm notice on the engine's stdout is noise to
// step over, not a reason to kill a live session. Only a TRANSPORT error ends
// the session and releases every parked caller.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
// child-obituary blindness an agentcoord debugging session once hit.
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
	// br frames the inbound stream by LINE, which is the framing ACP
	// specifies. Reading a whole line before parsing is what bounds the blast
	// radius of bad input to one frame — a streaming json.Decoder, by
	// contrast, stops mid-value on a syntax error at a byte offset nothing
	// downstream can resynchronise from, which is why this codec used to have
	// to end the session over one line of stdout noise.
	br     *bufio.Reader
	w      io.Writer
	closer io.Closer

	writeMu sync.Mutex // serializes frame writes (Call, Notify, and inbound responses share the writer)
	// nextID carries its own 64-bit alignment guarantee. A bare int64 reached
	// with atomic.AddInt64 does not: Go promises 8-byte alignment only for the
	// first word of an allocated struct on 32-bit platforms, and this field is
	// several words in, so the add would panic there — and would start or stop
	// doing so with any future reordering of the fields above it.
	nextID atomic.Int64

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
//
// closer is the connection's ONLY teardown lever, and it carries a
// requirement the type cannot check: it must end the stream r reads from, so
// that a Read parked in the read loop returns. A closer that shuts the
// transport's write end (or the socket) satisfies this; one that merely
// releases bookkeeping does not. Pass nil only when this side is not meant to
// be able to tear the connection down — the read loop will then run until the
// peer ends the stream, and nothing on this side can stop it.
func NewConn(r io.Reader, w io.Writer, closer io.Closer, handler Handler) *Conn {
	return &Conn{
		br:      bufio.NewReader(r),
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
// ctx is the DISPATCH SCOPE handed to each handler invocation, NOT the
// connection's lifetime. Cancelling it does not stop the read loop: the loop
// spends its life parked inside Decode, where it can select on nothing, so
// the only lever is the closer — and pulling that lever is the OWNER's call to
// time, not this type's. An ACP client cancelling a turn has protocol work to
// do on the wire first (it sends session/cancel and lets the parked
// session/prompt resolve with stopReason "cancelled", keeping the session
// usable); a connection that tore itself down the instant its ctx ended would
// race that notification off the wire. Callers that want cancellation to end
// the connection compose it themselves — cancel, say what the protocol
// requires, then Close.
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
	// A frame with no method is garbage the peer must drop: refuse
	// it here rather than emitting {"jsonrpc":"2.0"} and reporting success.
	if method == "" {
		return nil, errors.New("acp: refusing to send a request with no method")
	}
	rawParams, perr := marshalParams(method, params)
	if perr != nil {
		return nil, perr
	}
	id := c.nextID.Add(1)
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
		// Everything this codec itself reports is annotated with the RPC it
		// belongs to: one connection multiplexes every method, so a bare
		// "context deadline exceeded" names no request and cannot be acted on.
		// The peer's OWN error object is the exception — it travels verbatim so
		// callers can keep type-asserting *Error and reading its code.
		select {
		case resp, ok := <-ch:
			if !ok {
				return rpcErrf(method, id, c.closedErr()) // failPending closed the slot (connection died)
			}
			if resp.Error != nil {
				return resp.Error
			}
			if result != nil && len(resp.Result) > 0 {
				if uerr := json.Unmarshal(resp.Result, result); uerr != nil {
					return rpcErrf(method, id, uerr)
				}
				return nil
			}
			return nil
		case <-ctx.Done():
			return rpcErrf(method, id, ctx.Err())
		case <-c.done:
			return rpcErrf(method, id, c.closedErr())
		}
	}
	return await, nil
}

// rpcErrf attributes a codec-side failure to the request it belongs to. The
// cause is wrapped, not replaced, so errors.Is on ErrConnClosed or a context
// cause keeps working for callers that branch on it.
func rpcErrf(method string, id int64, cause error) error {
	return fmt.Errorf("acp: %s (request id %d): %w", method, id, cause)
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

// Close runs the closer given to NewConn, once. Whether that unblocks the read
// loop and the parked callers is the CLOSER's property, not this type's: a Conn
// owns neither the reader nor the writer and has no way to interrupt a Read in
// progress. With a closer that ends the stream, Close is a full teardown; with
// a nil closer there is nothing to run and Close reports success having stopped
// nothing, which is the caller's choice to make and not a failure to report.
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

// readLoop reads newline-delimited frames until the TRANSPORT ends, routing
// each to a pending caller (response), the handler (request/notification), or
// a warning (garbage). Nothing the peer can put in a line ends the session:
// only a read error does, and that fails every parked caller and closes done.
func (c *Conn) readLoop(ctx context.Context) {
	defer close(c.done)
	for {
		line, err := c.readFrameLine()
		// A stream may hand back a final unterminated frame together with its
		// error, so the line is served before the error is acted on.
		if len(line) > 0 {
			c.dispatch(ctx, line)
		}
		if err != nil {
			c.readErr.Store(&err)
			c.failPending()
			return
		}
	}
}

// readFrameLine returns the next frame's bytes with the terminator and any
// surrounding whitespace stripped, which for a blank line is nothing at all.
// It deliberately imposes NO size ceiling: ACP carries base64 image blocks,
// and silently truncating one is worse than the memory it costs to read it.
func (c *Conn) readFrameLine() ([]byte, error) {
	line, err := c.br.ReadBytes('\n')
	return bytes.TrimSpace(line), err
}

// dispatch routes one frame's worth of bytes. A line it cannot read as a
// JSON-RPC message is warned about and stepped over — on a newline-delimited
// wire the next frame boundary is the next '\n', so unreadable input costs
// exactly the line it arrived on. Engines emit real non-JSON on stdout
// ("Debugger attached", an npm notice, a stray console.log from a
// user-configured adapter); a live session must survive all of it.
func (c *Conn) dispatch(ctx context.Context, line []byte) {
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		warnf("acp: dropping a line that is not a JSON-RPC frame and continuing: %v", err)
		return
	}
	// The spec makes this member MUST-be-exactly-"2.0". A mismatch means the
	// peer is not speaking the protocol we are, which is worth saying out
	// loud on the first frame that shows it — but not worth dropping the
	// peer's traffic over, since everything else about the frame may still
	// be serviceable.
	if m.JSONRPC != jsonrpcVersion {
		warnf("acp: peer frame declares jsonrpc %q, not %q", m.JSONRPC, jsonrpcVersion)
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

// serveNotification dispatches an inbound notification with the same panic
// recovery serveRequest gives requests. A notification has no
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
	// HandleRequest runs INLINE on this read-loop goroutine. Without a
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
	// JSON-RPC 2.0 mandates a null id on an error the peer cannot attribute to
	// a request — Parse error (-32700), Invalid Request (-32600) — which is to
	// say, on the errors reporting that OUR OWN output was unreadable. No
	// caller can be waiting on it (ids start at 1), and unmarshalling JSON null
	// into an int64 is a no-op in encoding/json, so routing it by id would
	// silently rename it "unknown id 0" and discard the code and message.
	if isNullID(m.ID) {
		if m.Error != nil {
			warnf("acp: peer reported an error it could not attribute to a request: %s", m.Error.Error())
		} else {
			warnf("acp: dropping id-less response frame carrying no error")
		}
		return
	}
	id, ok := responseID(m.ID)
	if !ok {
		warnf("acp: dropping response with an id that matches no outstanding request: %s", m.ID)
		return
	}
	c.pendingMu.Lock()
	ch := c.pending[id]
	c.pendingMu.Unlock()
	if ch == nil {
		warnf("acp: dropping response for unknown id %d", id)
		return
	}
	// The slot is cap-1 and JSON-RPC 2.0 permits exactly one response per id,
	// so a full slot means the peer answered twice. This send runs on the read
	// loop: blocking here stops every later frame, permanently, because the
	// waiting caller receives once and then abandons the slot. First answer
	// wins; the extra is dropped.
	select {
	case ch <- m:
	default:
		warnf("acp: dropping duplicate response for id %d (one response per id was already routed)", id)
	}
}

// responseID resolves a response frame's id member to the integer id this
// codec allocated for the waiting caller. Matching is deliberately lenient
// about SPELLING, because every spelling below denotes the same id we sent and
// refusing one parks the caller until its deadline for a turn that actually
// completed: JSON-RPC 2.0 permits a String id, so a peer echoing 1 back as
// "1"; and JSON numbers have no integer type, so a peer whose runtime holds
// ids as floats echoes 1 back as 1.0 or 1e0. Only the MATCHING side is
// lenient — outbound ids stay integers. false means the member does not denote
// an integer at all.
func responseID(raw json.RawMessage) (int64, bool) {
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return integralID(num)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	return integralID(json.Number(s))
}

// integralID reads a JSON number as the integer id it denotes. Int64 covers
// the spellings that are already integers; the float fallback covers the ones
// that are not written as integers but denote one, and rejects both a genuine
// fraction and a magnitude no int64 can hold — for which the conversion below
// would otherwise be undefined rather than wrong-but-detectable.
func integralID(num json.Number) (int64, bool) {
	if id, err := num.Int64(); err == nil {
		return id, true
	}
	f, err := num.Float64()
	if err != nil || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}

// isNullID reports whether a frame's id member is the JSON literal null. The
// decoder stores a raw member verbatim, so this is a byte compare; the wire
// distinction matters because null is a PRESENT id member meaning "not
// attributable", not an absent one.
func isNullID(raw json.RawMessage) bool { return string(raw) == "null" }

// failPending unblocks every parked caller when the connection dies.
func (c *Conn) failPending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		close(ch) // the await detects the closed slot and returns closedErr
		delete(c.pending, id)
	}
}

// closedErr renders the reason a parked caller was released. An end-of-stream
// is a normal hangup and reports as ErrConnClosed; anything else is a real
// transport failure and travels verbatim. EOF is matched with errors.Is
// because a reader in the chain may annotate it, and an annotated hangup is
// still a hangup.
func (c *Conn) closedErr() error {
	if p := c.readErr.Load(); p != nil && *p != nil && !errors.Is(*p, io.EOF) {
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
// silently removed, which the peer answers as best it can.
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
// succeeded while handing it zero payload is the house lie in wire form.
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
