package agentbus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// The executor→orchestrator transport: a child engine's own `ctxloom mcp`
// subprocess forwards agent_send/agent_recv here over a Unix socket (path from
// SocketEnv). The protocol is package-internal — one JSON request line per
// connection, one JSON response line back — and MUST NOT leak into any
// exported wire contract.

// busRequest is one forwarded tool call. From is the caller's ambient
// identity (its CTXLOOM_SESSION_HARP); the orchestrator's lineage — not the
// caller — decides where "parent" routes.
type busRequest struct {
	Op     string `json:"op"` // "send" | "recv"
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Body   string `json:"body,omitempty"`
	WaitMS int64  `json:"wait_ms,omitempty"`
}

type busResponse struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	ErrKind  string    `json:"error_kind,omitempty"`
	Messages []Message `json:"messages,omitempty"`
}

// errKind wire tags, mapped back to the typed sentinels client-side so a
// forwarded failure is indistinguishable from a local one.
const (
	errKindTimeout = "timeout"
	errKindRouting = "routing"
	errKindSender  = "unknown-sender"
	errKindBusy    = "busy"
)

// maxBusLine bounds one protocol line (a briefing-sized body fits comfortably;
// bulk data never rides the bus — it goes to the durable stores).
const maxBusLine = 4 * 1024 * 1024

// recvDialGrace pads the client's read deadline past the server-side wait so
// the server's timer, not the socket, decides a recv timeout.
const recvDialGrace = 15 * time.Second

// Server serves a broker over a Unix socket for forwarded executor calls.
type Server struct {
	ln     net.Listener
	broker *Broker
	ctx    context.Context
	cancel context.CancelFunc
}

// Listen binds the broker to a Unix socket at path, replacing any stale
// socket file from a dead predecessor. The accept loop runs until Close.
func Listen(path string, broker *Broker) (*Server, error) {
	// A leftover socket file refuses the bind; a dead orchestrator can't be
	// listening, so unlinking is safe.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("agent bus: listen %s: %w", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, broker: broker, ctx: ctx, cancel: cancel}
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
	default:
		return busResponse{Error: fmt.Sprintf("unknown bus op %q", req.Op)}
	}
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
	default:
		return errors.New(resp.Error)
	}
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
