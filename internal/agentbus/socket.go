// Package agentbus is the OBSERVATION socket for ctxloom's agent delegation:
// the viewer verbs observe/roster/inject over a Unix socket under the serving
// session's dir. The message-bus verbs (send/recv) and the in-memory broker
// RETIRED with the agentcoord Wave B1 migration — durable role mailboxes live
// in the runtime coordinator (internal/agentcoord/coord), children reach it
// over authenticated streamable-HTTP MCP, and this socket survives ONLY for
// the viewer until Wave D re-points observation onto the plane-1 event log.
// The protocol is package-internal — one JSON request line per connection,
// one JSON response line back (the observe op alone continues past its ack as
// a stream of ObserveEvent lines) — and MUST NOT leak into any exported wire
// contract.
package agentbus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ErrNotInjectable rejects a user injection whose target the coordinator does
// not hold and cannot resume (an unknown harp, or a foreign process's session
// — there is no delivery channel into another process's terminal, by design).
var ErrNotInjectable = errors.New("inject: target is not a child this coordinator holds or can resume")

// busRequest is one viewer call.
type busRequest struct {
	Op   string `json:"op"` // "observe" | "roster" | "inject"
	Body string `json:"body,omitempty"`
	Harp string `json:"harp,omitempty"` // observe/inject: the target harp
}

type busResponse struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	ErrKind  string        `json:"error_kind,omitempty"`
	Roster   []RosterEntry `json:"roster,omitempty"`
	Delivery string        `json:"delivery,omitempty"` // inject: the Delivery* mode applied
}

// Delivery modes an inject reports back (busResponse.Delivery): which §6a
// delivery-by-state rule the coordinator applied to the user's text. The
// viewer renders these verbatim.
const (
	DeliveryCompletedRecv = "completed-recv" // completed the child's parked agent_recv
	DeliveryNewTurn       = "new-turn"       // woke an idle child into a new turn
	DeliveryQueued        = "queued"         // queued for the child's next turn boundary
	DeliveryResumed       = "resumed"        // relaunched an ended session, the text as its next turn
)

// ObserveEvent is one line on an observation stream (the observe verb's
// vocabulary, after the ack). Exactly one of Entry, Complete, Gap, or Ended is
// set per line: Entry/Complete mirror the child's live ChatEvents; Gap reports
// events the observer's bounded buffer dropped; Ended closes the stream.
type ObserveEvent struct {
	Entry    *agent.SessionEntry `json:"entry,omitempty"`
	Complete *agent.TurnMeta     `json:"complete,omitempty"`
	Gap      int                 `json:"gap,omitempty"`
	Ended    bool                `json:"ended,omitempty"`
}

// RosterEntry is one session in a coordinator's roster: a delegation child it
// holds, with its lineage and coordinator-visible state. State is one of
// queued|executing|parked|idle|ended ("parked" = parked in agent_recv).
type RosterEntry struct {
	Harp             string `json:"harp"`
	Agent            string `json:"agent,omitempty"`
	State            string `json:"state"`
	Parent           string `json:"parent,omitempty"`
	LastActivityUnix int64  `json:"last_activity_unix,omitempty"`
}

// RosterFunc supplies the roster snapshot on demand. The coordinator owns the
// child-state folds, so the server asks it rather than mirroring state (one
// source of truth, two transports).
type RosterFunc func() []RosterEntry

// InjectFunc delivers user-typed text into a session the coordinator holds,
// reporting the Delivery* mode that applied.
type InjectFunc func(harp, text string) (string, error)

// errKind wire tags, mapped back to the typed sentinels client-side so a
// forwarded failure is indistinguishable from a local one.
const (
	errKindNotLive       = "not-live"
	errKindNotInjectable = "not-injectable"
)

// maxBusLine bounds one protocol line.
const maxBusLine = 4 * 1024 * 1024

// dialGrace bounds the request/ack round trip.
const dialGrace = 15 * time.Second

// Server serves the observation verbs over a Unix socket: observe over hub,
// roster over the snapshot func, inject delegated to the coordinator. hub,
// roster, and inject may be nil: observe then answers not-live, roster
// answers empty, and inject answers not-injectable.
type Server struct {
	ln     net.Listener
	hub    *TapHub
	roster RosterFunc
	inject InjectFunc
	ctx    context.Context
	cancel context.CancelFunc
}

// Listen binds the observation surfaces to a Unix socket at path, replacing
// any stale socket file from a dead predecessor. The accept loop runs until
// Close.
func Listen(path string, hub *TapHub, roster RosterFunc, inject InjectFunc) (*Server, error) {
	// A leftover socket file refuses the bind; a dead coordinator can't be
	// listening, so unlinking is safe.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("agent bus: listen %s: %w", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, hub: hub, roster: roster, inject: inject, ctx: ctx, cancel: cancel}
	go s.acceptLoop()
	return s, nil
}

// Addr returns the socket path the server is bound to.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the accept loop and ends open observation streams.
func (s *Server) Close() error {
	s.cancel()
	return s.ln.Close()
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := readBusLine(r)
	if err != nil {
		return
	}
	var req busRequest
	if err := json.Unmarshal(line, &req); err != nil {
		writeBusResponse(conn, busResponse{Error: "malformed bus request: " + err.Error()})
		return
	}
	if req.Op == "observe" {
		s.serveObserve(conn, req)
		return
	}
	writeBusResponse(conn, s.dispatch(req))
}

func (s *Server) dispatch(req busRequest) busResponse {
	switch req.Op {
	case "roster":
		var entries []RosterEntry
		if s.roster != nil {
			entries = s.roster()
		}
		return busResponse{OK: true, Roster: entries}
	case "inject":
		if s.inject == nil {
			return errResponse(ErrNotInjectable)
		}
		mode, err := s.inject(req.Harp, req.Body)
		if err != nil {
			return errResponse(err)
		}
		return busResponse{OK: true, Delivery: mode}
	case "send", "recv":
		// Retired with the shim: children reach the coordinator over its
		// authenticated MCP endpoint now; the socket keeps only the viewer.
		return busResponse{Error: "bus op " + req.Op + " was retired: agent_send/agent_recv ride the coordinator MCP endpoint (CTXLOOM_COORD_URL)"}
	default:
		return busResponse{Error: fmt.Sprintf("unknown bus op %q", req.Op)}
	}
}

// serveObserve upgrades the connection to an observation stream: after the
// ack line, ObserveEvent lines flow until the tap ends, the client
// disconnects, or the server closes. Only CHILDREN the coordinator holds
// live are tappable.
func (s *Server) serveObserve(conn net.Conn, req busRequest) {
	if s.hub == nil {
		writeBusResponse(conn, errResponse(ErrNotLive))
		return
	}
	ob, err := s.hub.Subscribe(req.Harp)
	if err != nil {
		writeBusResponse(conn, errResponse(err))
		return
	}
	defer ob.Close()
	writeBusResponse(conn, busResponse{OK: true})

	// A client disconnect (its EOF) must release the subscription promptly
	// even while the feed idles; the protocol sends nothing after the
	// request, so any read completion means the client is gone.
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()

	for {
		dropped, events, ok, terr := ob.Take(ctx)
		if terr != nil {
			return
		}
		for _, line := range observeLines(dropped, events) {
			if werr := writeObserveEvent(conn, line); werr != nil {
				return
			}
		}
		if !ok {
			_ = writeObserveEvent(conn, ObserveEvent{Ended: true})
			return
		}
	}
}

// observeLines maps drained ChatEvents onto the observe wire vocabulary: a
// standalone gap marker first when the observer's ring overflowed, then one
// line per content event. Only Entry and Complete map — Session/Permission
// events are engine-lifecycle chatter with no transcript shape (and headless
// children never forward permissions), so they are skipped, not gap-counted.
func observeLines(dropped int, events []agent.ChatEvent) []ObserveEvent {
	out := make([]ObserveEvent, 0, len(events)+1)
	if dropped > 0 {
		out = append(out, ObserveEvent{Gap: dropped})
	}
	for _, ev := range events {
		switch {
		case ev.Entry != nil:
			out = append(out, ObserveEvent{Entry: ev.Entry})
		case ev.Complete != nil:
			out = append(out, ObserveEvent{Complete: ev.Complete})
		}
	}
	return out
}

func writeObserveEvent(conn net.Conn, ev ObserveEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(payload, '\n'))
	return err
}

func errResponse(err error) busResponse {
	resp := busResponse{Error: err.Error()}
	switch {
	case errors.Is(err, ErrNotLive):
		resp.ErrKind = errKindNotLive
	case errors.Is(err, ErrNotInjectable):
		resp.ErrKind = errKindNotInjectable
	}
	return resp
}

// respError maps a wire error back to its typed sentinel, wrapped so the
// original server-rendered text still reaches the caller.
func respError(resp busResponse) error {
	if resp.OK {
		return nil
	}
	switch resp.ErrKind {
	case errKindNotLive:
		return ErrNotLive
	case errKindNotInjectable:
		return ErrNotInjectable
	default:
		return errors.New(resp.Error)
	}
}

// Inject sends user-typed text into harp via the coordinator at sock — the
// viewer's inject verb. The returned mode is the Delivery* constant the
// coordinator reports; a target it doesn't hold and can't resume maps back
// to the typed ErrNotInjectable.
func Inject(sock, harp, text string) (string, error) {
	resp, err := roundTrip(sock, busRequest{Op: "inject", Harp: harp, Body: text}, dialGrace)
	if err != nil {
		return "", err
	}
	if err := respError(resp); err != nil {
		return "", err
	}
	return resp.Delivery, nil
}

// FetchRoster lists the coordinator's held sessions over the bus socket at
// sock — the observation viewer's roster source.
func FetchRoster(sock string) ([]RosterEntry, error) {
	resp, err := roundTrip(sock, busRequest{Op: "roster"}, dialGrace)
	if err != nil {
		return nil, err
	}
	if err := respError(resp); err != nil {
		return nil, err
	}
	return resp.Roster, nil
}

// ObserveSession subscribes to a harp's live tap on the coordinator at sock.
// Events stream on the returned channel until the tap ends (a final Ended
// event), ctx is cancelled, or the connection drops; errs (buffered) carries a
// terminal transport error. ErrNotLive when that coordinator does not hold
// the harp's event stream — the caller's cue to tail the store instead.
func ObserveSession(ctx context.Context, sock, harp string) (<-chan ObserveEvent, <-chan error, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("agent bus: dial coordinator %s: %w", sock, err)
	}
	payload, err := json.Marshal(busRequest{Op: "observe", Harp: harp})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	// The ack is bounded like any round trip; the stream after it lives until
	// the tap ends, so the deadline is lifted once subscribed.
	_ = conn.SetDeadline(time.Now().Add(dialGrace))
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("agent bus: send request: %w", err)
	}
	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := readBusLine(r)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("agent bus: read response: %w", err)
	}
	var resp busResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("agent bus: malformed response: %w", err)
	}
	if err := respError(resp); err != nil {
		conn.Close()
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	events := make(chan ObserveEvent)
	errs := make(chan error, 1)
	readerDone := make(chan struct{})
	// Cancellation watcher: closing the conn is what unblocks a parked read.
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-readerDone:
		}
	}()
	go func() {
		defer close(errs)
		defer close(events)
		defer close(readerDone)
		defer conn.Close()
		for {
			line, rerr := readBusLine(r)
			if rerr != nil {
				// EOF right after Ended is the clean close; anything else on a
				// live ctx is a transport fault the consumer should hear.
				if rerr != io.EOF && ctx.Err() == nil {
					errs <- fmt.Errorf("agent bus: observation stream: %w", rerr)
				}
				return
			}
			var ev ObserveEvent
			if uerr := json.Unmarshal(line, &ev); uerr != nil {
				errs <- fmt.Errorf("agent bus: malformed observation event: %w", uerr)
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
			if ev.Ended {
				return
			}
		}
	}()
	return events, errs, nil
}

func roundTrip(sock string, req busRequest, deadline time.Duration) (busResponse, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return busResponse{}, fmt.Errorf("agent bus: dial coordinator %s: %w", sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(deadline))
	payload, err := json.Marshal(req)
	if err != nil {
		return busResponse{}, err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return busResponse{}, fmt.Errorf("agent bus: send request: %w", err)
	}
	line, err := readBusLine(bufio.NewReaderSize(conn, 64*1024))
	if err != nil {
		return busResponse{}, fmt.Errorf("agent bus: read response: %w", err)
	}
	var resp busResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return busResponse{}, fmt.Errorf("agent bus: malformed response: %w", err)
	}
	return resp, nil
}

// readBusLine reads one newline-terminated protocol line, bounded by
// maxBusLine.
func readBusLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > maxBusLine {
			return nil, fmt.Errorf("agent bus: request line exceeds %d bytes", maxBusLine)
		}
		if !isPrefix {
			return line, nil
		}
	}
}

func writeBusResponse(conn net.Conn, resp busResponse) {
	payload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(payload, '\n'))
}
