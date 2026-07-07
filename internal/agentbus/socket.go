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

// The executor→orchestrator transport: a child engine's own `ctxloom mcp`
// subprocess forwards agent_send/agent_recv here over a Unix socket (path from
// SocketEnv), and observation clients (the feed resolver, the future viewer)
// dial the same socket for the observe/roster verbs. The protocol is
// package-internal — one JSON request line per connection, one JSON response
// line back (the observe op alone continues past its ack as a stream of
// ObserveEvent lines) — and MUST NOT leak into any exported wire contract.

// busRequest is one forwarded tool call. From is the caller's ambient
// identity (its CTXLOOM_SESSION_HARP); the orchestrator's lineage — not the
// caller — decides where "parent" routes.
type busRequest struct {
	Op     string `json:"op"` // "send" | "recv" | "observe" | "roster"
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Body   string `json:"body,omitempty"`
	WaitMS int64  `json:"wait_ms,omitempty"`
	Harp   string `json:"harp,omitempty"` // observe: the harp to tap
}

type busResponse struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	ErrKind  string        `json:"error_kind,omitempty"`
	Messages []Message     `json:"messages,omitempty"`
	Roster   []RosterEntry `json:"roster,omitempty"`
}

// ObserveEvent is one line on an observation stream (the observe verb's
// vocabulary, after the ack). Exactly one of Entry, Complete, Gap, or Ended is
// set per line: Entry/Complete mirror the child's live ChatEvents (Session/
// Permission chatter does not map — see observeLines); Gap reports events the
// observer's bounded buffer dropped; Ended closes the stream (the child's
// engine exited or was torn down).
type ObserveEvent struct {
	Entry    *agent.SessionEntry `json:"entry,omitempty"`
	Complete *agent.TurnMeta     `json:"complete,omitempty"`
	Gap      int                 `json:"gap,omitempty"`
	Ended    bool                `json:"ended,omitempty"`
}

// RosterEntry is one session in an orchestrator's roster: a delegation child
// it holds, with its lineage and coordinator-visible state. State is one of
// queued|executing|parked|idle|ended ("parked" = parked in agent_recv).
type RosterEntry struct {
	Harp             string `json:"harp"`
	Agent            string `json:"agent,omitempty"`
	State            string `json:"state"`
	Parent           string `json:"parent,omitempty"`
	LastActivityUnix int64  `json:"last_activity_unix,omitempty"`
}

// RosterFunc supplies the roster snapshot on demand. The orchestrator owns the
// child-state bookkeeping, so the server asks it rather than mirroring state
// into the broker (one source of truth; a future agent_list MCP tool shares
// the same snapshot).
type RosterFunc func() []RosterEntry

// errKind wire tags, mapped back to the typed sentinels client-side so a
// forwarded failure is indistinguishable from a local one.
const (
	errKindTimeout = "timeout"
	errKindRouting = "routing"
	errKindSender  = "unknown-sender"
	errKindBusy    = "busy"
	errKindNotLive = "not-live"
)

// maxBusLine bounds one protocol line (a briefing-sized body fits comfortably;
// bulk data never rides the bus — it goes to the durable stores).
const maxBusLine = 4 * 1024 * 1024

// recvDialGrace pads the client's read deadline past the server-side wait so
// the server's timer, not the socket, decides a recv timeout.
const recvDialGrace = 15 * time.Second

// Server serves a broker over a Unix socket for forwarded executor calls,
// plus the observation verbs (observe over hub, roster over the snapshot
// func). hub and roster may be nil: observe then answers not-live and roster
// answers empty.
type Server struct {
	ln     net.Listener
	broker *Broker
	hub    *TapHub
	roster RosterFunc
	ctx    context.Context
	cancel context.CancelFunc
}

// Listen binds the broker (and the observation surfaces) to a Unix socket at
// path, replacing any stale socket file from a dead predecessor. The accept
// loop runs until Close.
func Listen(path string, broker *Broker, hub *TapHub, roster RosterFunc) (*Server, error) {
	// A leftover socket file refuses the bind; a dead orchestrator can't be
	// listening, so unlinking is safe.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("agent bus: listen %s: %w", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, broker: broker, hub: hub, roster: roster, ctx: ctx, cancel: cancel}
	go s.acceptLoop()
	return s, nil
}

// Addr returns the socket path the server is bound to.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the accept loop and unblocks parked forwarded receives.
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

// serveConn handles one forwarded call: read the request line, dispatch to
// the broker (a recv parks right here, holding the connection open — that
// parked call is what yields the child's execution slot), write the response.
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
	case "send":
		if err := s.broker.SendFromChild(req.From, req.To, req.Kind, req.Body); err != nil {
			return errResponse(err)
		}
		return busResponse{OK: true}
	case "recv":
		msgs, err := s.broker.Recv(s.ctx, req.From, time.Duration(req.WaitMS)*time.Millisecond)
		if err != nil {
			return errResponse(err)
		}
		return busResponse{OK: true, Messages: msgs}
	case "roster":
		var entries []RosterEntry
		if s.roster != nil {
			entries = s.roster()
		}
		return busResponse{OK: true, Roster: entries}
	default:
		return busResponse{Error: fmt.Sprintf("unknown bus op %q", req.Op)}
	}
}

// serveObserve upgrades the connection to an observation stream: after the
// ack line, ObserveEvent lines flow until the tap ends, the client
// disconnects, or the server closes. Only CHILDREN this orchestrator holds
// live are tappable — the orchestrator does not drive its own serving
// session's engine, so the coordinator harp is always not-live here.
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
	case errors.Is(err, ErrRecvTimeout):
		resp.ErrKind = errKindTimeout
	case errors.Is(err, ErrPeerRouting):
		resp.ErrKind = errKindRouting
	case errors.Is(err, ErrUnknownSender):
		resp.ErrKind = errKindSender
	case errors.Is(err, ErrRecvBusy):
		resp.ErrKind = errKindBusy
	case errors.Is(err, ErrNotLive):
		resp.ErrKind = errKindNotLive
	}
	return resp
}

// ForwardSend forwards one agent_send from a child's ctxloom-mcp process to
// the orchestrator's broker at sock. from is the caller's ambient harp.
func ForwardSend(sock, from, to, kind, body string) error {
	resp, err := roundTrip(sock, busRequest{Op: "send", From: from, To: to, Kind: kind, Body: body}, recvDialGrace)
	if err != nil {
		return err
	}
	return respError(resp)
}

// ForwardRecv forwards one agent_recv, parking server-side for up to wait.
func ForwardRecv(sock, from string, wait time.Duration) ([]Message, error) {
	resp, err := roundTrip(sock, busRequest{Op: "recv", From: from, WaitMS: wait.Milliseconds()}, wait+recvDialGrace)
	if err != nil {
		return nil, err
	}
	if err := respError(resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

// respError maps a wire error back to its typed sentinel, wrapped so the
// original server-rendered text still reaches the caller.
func respError(resp busResponse) error {
	if resp.OK {
		return nil
	}
	switch resp.ErrKind {
	case errKindTimeout:
		return ErrRecvTimeout
	case errKindRouting:
		return ErrPeerRouting
	case errKindSender:
		return ErrUnknownSender
	case errKindBusy:
		return ErrRecvBusy
	case errKindNotLive:
		return ErrNotLive
	default:
		return errors.New(resp.Error)
	}
}

// FetchRoster lists the orchestrator's held sessions over the bus socket at
// sock — the observation viewer's roster source.
func FetchRoster(sock string) ([]RosterEntry, error) {
	resp, err := roundTrip(sock, busRequest{Op: "roster"}, recvDialGrace)
	if err != nil {
		return nil, err
	}
	if err := respError(resp); err != nil {
		return nil, err
	}
	return resp.Roster, nil
}

// ObserveSession subscribes to a harp's live tap on the orchestrator at sock.
// Events stream on the returned channel until the tap ends (a final Ended
// event), ctx is cancelled, or the connection drops; errs (buffered) carries a
// terminal transport error. ErrNotLive when that orchestrator does not hold
// the harp's event stream — the caller's cue to tail the store instead.
func ObserveSession(ctx context.Context, sock, harp string) (<-chan ObserveEvent, <-chan error, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("agent bus: dial orchestrator %s: %w", sock, err)
	}
	payload, err := json.Marshal(busRequest{Op: "observe", Harp: harp})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	// The ack is bounded like any round trip; the stream after it lives until
	// the tap ends, so the deadline is lifted once subscribed.
	_ = conn.SetDeadline(time.Now().Add(recvDialGrace))
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
		return busResponse{}, fmt.Errorf("agent bus: dial orchestrator %s: %w", sock, err)
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
