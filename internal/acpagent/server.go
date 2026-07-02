// Package acpagent exposes ctxloom AS an Agent Client Protocol agent: the
// server half of ctxloom's ACP story (the client half — driving other agents —
// is internal/acp). `ctxloom acp` serves newline-delimited JSON-RPC 2.0 over
// stdio, so ANY ACP client (Zed's agent panel, editor plugins) can drive
// ctxloom sessions: assembled context, profiles, and the configured engine,
// without a bespoke per-editor frontend.
//
// Slice 1 surface: initialize, session/new (one engine conversation per ACP
// session; cwd-scoped config), session/prompt (one turn at a time; ctxloom's
// assembled context rides the FIRST turn as a lead block — the same delivery
// model as the oneshot fan-out's lead fragment), session/cancel (tears the
// session down; the in-flight turn reports stopReason "cancelled"). Engine
// permissions are auto-approved, mirroring the non-interactive oneshot rule —
// forwarding session/request_permission to the OUTER client is the next slice.
package acpagent

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"

	"github.com/joshgarnett/agent-client-protocol-go/acp/api"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// agentName/agentVersion identify ctxloom in the initialize response.
const (
	agentName    = "ctxloom"
	agentVersion = "1.0.0"
)

// EngineChat is one live engine conversation backing an ACP session: the
// assembled context to deliver on the first turn, the message-in/events-out
// channels of the engine's structured chat, and its teardown.
type EngineChat struct {
	// Context is ctxloom's assembled context for this session's cwd/profile,
	// prepended to the first prompt as a lead block ("" = none).
	Context string
	// In carries user messages into the engine; the server never closes it
	// except through Close.
	In chan<- string
	// Events streams the engine's normalized chat events; closed when the
	// conversation ends.
	Events <-chan agent.ChatEvent
	// Errs reports a conversation-fatal error (the pb chat error channel).
	Errs <-chan error
	// Close tears the engine conversation down (idempotent).
	Close func()
}

// ChatOpener opens the engine conversation for a new ACP session rooted at
// cwd. The production opener (internal/cli) loads ctxloom config from cwd,
// assembles context, and opens the plugin's Chat stream; tests inject fakes.
type ChatOpener func(ctx context.Context, cwd string) (*EngineChat, error)

// Server is the ACP agent: a jsonrpc.Handler over one client connection.
type Server struct {
	open ChatOpener

	ctx  context.Context
	conn *jsonrpc.Conn

	mu       sync.Mutex
	sessions map[api.SessionId]*session
	nextID   int64
}

// session is one ACP session bound to one engine conversation.
type session struct {
	id     api.SessionId
	engine *EngineChat
	cancel context.CancelFunc // cancels the session's engine ctx

	mu          sync.Mutex
	inTurn      bool
	cancelled   bool
	contextSent bool
	// tool-call id pairing: ToolUse pushes a generated id per tool name;
	// ToolResult pops its match so tool_call_update targets the right call.
	toolSeq  int64
	openCall map[string][]api.ToolCallId
}

// Serve runs the ACP agent over one reader/writer pair (stdio) until the
// client disconnects or ctx is cancelled. Every session's engine conversation
// is torn down on exit.
func Serve(ctx context.Context, r io.Reader, w io.Writer, open ChatOpener) error {
	s := &Server{open: open, ctx: ctx, sessions: make(map[api.SessionId]*session)}
	s.conn = jsonrpc.NewConn(ctx, r, w, nil, s)
	defer s.closeAllSessions()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.conn.Done():
		return nil // client disconnected (EOF) — a normal end
	}
}

// HandleRequest dispatches one inbound client request. initialize replies
// inline; session/new and session/prompt reply ASYNCHRONOUSLY (spawning the
// engine / running a whole turn must not block the read loop, or
// session/cancel could never be read mid-turn).
func (s *Server) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.MethodInitialize:
		reply(api.InitializeResponse{
			ProtocolVersion:   api.ACPProtocolVersion,
			AgentCapabilities: api.AgentCapabilities{},
		}, nil)
	case api.MethodSessionNew:
		go s.handleSessionNew(params, reply)
	case api.MethodSessionPrompt:
		go s.handlePrompt(params, reply)
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ctxloom acp: method not supported: " + method})
	}
}

// HandleNotification handles session/cancel; anything else is dropped with a
// warning (never crash the connection on an unmodeled frame).
func (s *Server) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	if method != api.MethodSessionCancel {
		clidiag.Warn("ctxloom", "acp agent: dropping notification %q", method)
		return
	}
	var n api.CancelNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return
	}
	s.cancelSession(n.SessionId)
}

// handleSessionNew opens the engine conversation for the request's cwd and
// registers the session. Runs off the read loop; replies exactly once.
func (s *Server) handleSessionNew(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	// mcpServers is acknowledged but unused in slice 1: ctxloom sessions carry
	// their OWN MCP config (profiles); merging client-supplied servers in is a
	// later, deliberate addition.
	if len(req.McpServers) > 0 {
		clidiag.Warn("ctxloom", "acp agent: ignoring %d client-supplied MCP servers (ctxloom's own MCP config applies)", len(req.McpServers))
	}

	ctx, cancel := context.WithCancel(s.ctx)
	engine, err := s.open(ctx, req.Cwd)
	if err != nil {
		cancel()
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "open ctxloom session: " + err.Error()})
		return
	}

	s.mu.Lock()
	s.nextID++
	sess := &session{
		id:       api.SessionId("ctxloom-" + strconv.FormatInt(s.nextID, 10)),
		engine:   engine,
		cancel:   cancel,
		openCall: make(map[string][]api.ToolCallId),
	}
	s.sessions[sess.id] = sess
	s.mu.Unlock()

	reply(api.NewSessionResponse{SessionId: sess.id}, nil)
}

// handlePrompt runs ONE turn: deliver the message (context-prefixed on the
// first turn), forward the engine's events as session/update notifications,
// and reply with the stop reason when the turn completes. Runs off the read
// loop; replies exactly once.
func (s *Server) handlePrompt(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req struct {
		SessionId api.SessionId      `json:"sessionId"`
		Prompt    []api.ContentBlock `json:"prompt"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	sess := s.lookup(req.SessionId)
	if sess == nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown session " + string(req.SessionId)})
		return
	}

	text := promptText(req.Prompt)
	sess.mu.Lock()
	if sess.inTurn {
		sess.mu.Unlock()
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "a turn is already in flight for this session"})
		return
	}
	sess.inTurn = true
	if !sess.contextSent && sess.engine.Context != "" {
		// First turn: ctxloom's assembled context rides as the lead block —
		// the oneshot fan-out's proven delivery model, no engine flags needed.
		text = sess.engine.Context + "\n\n" + text
	}
	sess.contextSent = true
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.inTurn = false
		sess.mu.Unlock()
	}()

	// Send the message; a dead engine (closed conversation) surfaces on Errs.
	select {
	case sess.engine.In <- text:
	case err := <-sess.engine.Errs:
		reply(nil, engineError(err))
		return
	case <-s.ctx.Done():
		reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
		return
	}

	for {
		select {
		case ev, ok := <-sess.engine.Events:
			if !ok {
				// Conversation ended mid-turn: cancelled (session/cancel) or died.
				if sess.wasCancelled() {
					reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
				} else {
					reply(nil, engineError(<-sess.engine.Errs))
				}
				return
			}
			if ev.Complete != nil {
				reply(api.PromptResponse{StopReason: api.StopReasonEndTurn}, nil)
				return
			}
			for _, upd := range sess.mapEvent(ev) {
				if err := s.conn.Notify(api.MethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: upd}); err != nil {
					reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "notify: " + err.Error()})
					return
				}
			}
		case err := <-sess.engine.Errs:
			if sess.wasCancelled() {
				reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
			} else {
				reply(nil, engineError(err))
			}
			return
		case <-s.ctx.Done():
			reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
			return
		}
	}
}

// cancelSession marks the session cancelled and tears its engine down — the
// in-flight turn (if any) then completes with stopReason "cancelled", as the
// spec REQUIRES after session/cancel. Slice 1 is deliberately coarse: the
// whole session ends (the engine conversation has no per-turn cancel); the
// client starts a new session to continue.
func (s *Server) cancelSession(id api.SessionId) {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.cancelled = true
	sess.mu.Unlock()
	sess.cancel()
	sess.engine.Close()
}

// lookup returns the live session for id, or nil.
func (s *Server) lookup(id api.SessionId) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// closeAllSessions tears down every live engine conversation (server exit).
func (s *Server) closeAllSessions() {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = make(map[api.SessionId]*session)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.cancel()
		sess.engine.Close()
	}
}

func (sess *session) wasCancelled() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.cancelled
}

// engineError renders a conversation-fatal engine error as a JSON-RPC error
// (nil-safe: a closed Errs channel yields nil).
func engineError(err error) *jsonrpc.Error {
	msg := "engine conversation ended unexpectedly"
	if err != nil {
		msg = err.Error()
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: msg}
}

// promptText flattens a prompt's content blocks to text (non-text blocks are
// dropped; images/resources are a later addition).
func promptText(blocks []api.ContentBlock) string {
	var out string
	for _, b := range blocks {
		if b.Type == api.ContentBlockTypeText && b.Text != nil {
			if out != "" {
				out += "\n"
			}
			out += b.Text.Text
		}
	}
	return out
}

// sessionUpdateParams is the session/update notification body.
type sessionUpdateParams struct {
	SessionId api.SessionId     `json:"sessionId"`
	Update    api.SessionUpdate `json:"update"`
}
