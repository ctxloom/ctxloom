// Package acpagent exposes ctxloom AS an Agent Client Protocol agent: the
// server half of ctxloom's ACP story (the client half — driving other agents —
// is internal/acp). `ctxloom acp` serves newline-delimited JSON-RPC 2.0 over
// stdio, so ANY ACP client (Zed's agent panel, editor plugins) can drive
// ctxloom sessions: assembled context, profiles, and the configured engine,
// without a bespoke per-editor frontend.
//
// Protocol surface: initialize, session/new (one engine conversation per ACP
// session; cwd-scoped config; client mcpServers pass through to the engine;
// the session id is the ctxloom harp when session accounting is available),
// session/load (replays a recorded harp's history, then continues on a fresh
// engine conversation primed with it), session/prompt (one turn at a time;
// ctxloom's assembled context rides the FIRST turn as a lead block — the same
// delivery model as the oneshot fan-out's lead fragment), session/set_mode
// (ctxloom profile sets — the composed defaults, each profile, each subagent's
// composed set — surfaced as ACP session modes; a switch re-assembles the
// lead context for the next turn), session/cancel (cancels the in-flight TURN;
// the session stays usable and the prompt resolves with stopReason
// "cancelled"). Engine permission requests forward to the connected editor as
// session/request_permission — real interactive approvals in structured mode.
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

// methodSessionSetMode is the session-modes request the pinned SDK predates
// (its api package has no modes surface at all — see wire.go for the
// hand-rolled mode types).
const methodSessionSetMode = "session/set_mode"

// EngineChat is one live engine conversation backing an ACP session: the
// assembled context to deliver on the first turn, the message-in/events-out
// channels of the engine's structured chat, and its teardown.
type EngineChat struct {
	// Context is ctxloom's assembled context for this session's cwd/profile,
	// prepended to the first prompt as a lead block ("" = none).
	Context string
	// In carries messages into the engine (user turns, permission answers,
	// turn cancels); the server never closes it except through Close.
	In chan<- agent.ChatMessage
	// Events streams the engine's normalized chat events; closed when the
	// conversation ends.
	Events <-chan agent.ChatEvent
	// Errs reports a conversation-fatal error (the pb chat error channel).
	Errs <-chan error
	// Close tears the engine conversation down (idempotent).
	Close func()
	// Harp is the ctxloom session name backing this conversation; when set it
	// becomes the ACP session id, which is what makes the session addressable
	// by session/load later. "" (accounting unavailable) falls back to a
	// connection-local generated id.
	Harp string
	// Modes surfaces ctxloom profile sets as ACP session modes (nil = none):
	// the composed defaults, each installed profile, and each subagent's
	// composed profile set.
	Modes *SessionModes
	// AssembleMode re-assembles the lead context for a mode's profile set,
	// backing session/set_mode (nil = mode switching unsupported). A mode
	// switch changes the CONTEXT only — the engine is pinned at launch.
	AssembleMode func(ctx context.Context, mode SessionMode) (string, error)
	// Replay is the recorded history to replay to the client on session/load.
	Replay []agent.SessionEntry
}

// SessionModes describes the profile-set-backed ACP session modes of a session.
type SessionModes struct {
	Current   string
	Available []SessionMode
}

// SessionMode is one selectable mode: a profile set to assemble — the composed
// default set, one ctxloom profile, or a subagent's composed profiles.
type SessionMode struct {
	ID   string
	Name string
	// Profiles is the profile set this mode assembles; nil means the
	// configured defaults.
	Profiles []string
	// Engine is the mode's declared engine binding (subagent modes; "" = none).
	// Informational at set_mode time: the session's engine is pinned at
	// launch, so a differing engine warns rather than switches.
	Engine string
}

// OpenRequest describes the engine conversation an ACP session needs.
type OpenRequest struct {
	// Cwd roots the session: ctxloom config is discovered from here.
	Cwd string
	// MCPServers are the client-supplied session/new mcpServers, passed
	// through to the engine conversation.
	MCPServers []agent.ChatMCPServer
	// Profile selects the ctxloom profile ("" = the configured defaults).
	Profile string
	// ResumeHarp names a recorded ctxloom session to resume (session/load):
	// the opener replays its history and primes the fresh engine with it.
	ResumeHarp string
}

// ChatOpener opens the engine conversation for a new ACP session. The
// production opener (internal/cli) loads ctxloom config from the request's
// cwd, assembles context, and opens the plugin's Chat stream; tests inject
// fakes.
type ChatOpener func(ctx context.Context, req OpenRequest) (*EngineChat, error)

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
	ctx    context.Context    // the session's engine ctx (guards engine.In sends)
	cancel context.CancelFunc // cancels the session's engine ctx

	// cancelTurnCh signals the in-flight turn's runner that session/cancel
	// arrived; the RUNNER forwards the CancelTurn to the engine so it is
	// guaranteed to follow the turn's own message.
	cancelTurnCh chan struct{}

	mu            sync.Mutex
	inTurn        bool
	turnCancelled bool // session/cancel arrived for the in-flight turn
	closed        bool // the session was torn down (server exit)
	contextSent   bool
	leadContext   string        // pending first/next-turn lead block (mode switches replace it)
	modes         *SessionModes // session-local mode state (Current mutates on set_mode)
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
// inline; session/new, session/load, and session/set_mode reply ASYNCHRONOUSLY
// (spawning the engine / re-assembling context must not block the read loop).
// session/prompt REGISTERS the turn synchronously — so a session/cancel read
// after it is guaranteed to see the turn in flight — then runs it off the loop.
func (s *Server) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.MethodInitialize:
		reply(initializeResult{
			ProtocolVersion:   api.ACPProtocolVersion,
			AgentCapabilities: api.AgentCapabilities{LoadSession: true},
			AgentInfo:         agentInfoBlock{Name: agentName, Version: agentVersion},
		}, nil)
	case api.MethodSessionNew:
		go s.handleSessionNew(params, reply)
	case api.MethodSessionLoad:
		go s.handleSessionLoad(params, reply)
	case api.MethodSessionPrompt:
		s.handlePrompt(params, reply)
	case methodSessionSetMode:
		go s.handleSetMode(params, reply)
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
	s.cancelTurn(n.SessionId)
}

// handleSessionNew opens the engine conversation for the request's cwd and
// registers the session. Runs off the read loop; replies exactly once.
func (s *Server) handleSessionNew(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	sess, rerr := s.openSession(OpenRequest{Cwd: req.Cwd, MCPServers: mcpServersFromACP(req.McpServers)}, "")
	if rerr != nil {
		reply(nil, rerr)
		return
	}
	reply(newSessionResult{SessionId: sess.id, Modes: modeStateWire(sess.snapshotModes())}, nil)
}

// handleSessionLoad resumes a recorded ctxloom session: it opens a fresh
// engine conversation primed with the recorded history, REPLAYS that history
// to the client as session/update notifications (the spec's required order:
// replay first, then the response), and registers the session under its harp.
func (s *Server) handleSessionLoad(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.LoadSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	sess, rerr := s.openSession(OpenRequest{
		Cwd:        req.Cwd,
		MCPServers: mcpServersFromACP(req.McpServers),
		ResumeHarp: string(req.SessionId),
	}, req.SessionId)
	if rerr != nil {
		reply(nil, rerr)
		return
	}
	for _, entry := range sess.engine.Replay {
		for _, upd := range sess.replayEntry(entry) {
			if err := s.conn.Notify(api.MethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: upd}); err != nil {
				reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "replay: " + err.Error()})
				return
			}
		}
	}
	reply(loadSessionResult{Modes: modeStateWire(sess.snapshotModes())}, nil)
}

// openSession opens the engine conversation and registers the session. A
// fixed id (session/load) is honored — and must not already be live; "" mints
// one (the engine's harp when available, else a connection-local id).
func (s *Server) openSession(req OpenRequest, fixedID api.SessionId) (*session, *jsonrpc.Error) {
	if fixedID != "" && s.lookup(fixedID) != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "session already active: " + string(fixedID)}
	}

	ctx, cancel := context.WithCancel(s.ctx)
	engine, err := s.open(ctx, req)
	if err != nil {
		cancel()
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "open ctxloom session: " + err.Error()}
	}

	s.mu.Lock()
	id := fixedID
	if id == "" && engine.Harp != "" {
		id = api.SessionId(engine.Harp)
	}
	if id == "" || s.sessions[id] != nil {
		s.nextID++
		id = api.SessionId("ctxloom-" + strconv.FormatInt(s.nextID, 10))
	}
	sess := &session{
		id:           id,
		engine:       engine,
		ctx:          ctx,
		cancel:       cancel,
		cancelTurnCh: make(chan struct{}, 1),
		leadContext:  engine.Context,
		modes:        engine.Modes,
		openCall:     make(map[string][]api.ToolCallId),
	}
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	return sess, nil
}

// handlePrompt REGISTERS one turn synchronously on the read loop — a
// session/cancel notification read after this request is then guaranteed to
// see the turn in flight — and runs it off the loop (runTurn). Replies exactly
// once.
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
	sess.turnCancelled = false
	select {
	case <-sess.cancelTurnCh: // drop a stale signal from a cancel that raced a completed turn
	default:
	}
	if !sess.contextSent && sess.leadContext != "" {
		// First turn (or first after a mode switch): ctxloom's assembled
		// context rides as the lead block — the oneshot fan-out's proven
		// delivery model, no engine flags needed.
		text = sess.leadContext + "\n\n" + text
	}
	sess.contextSent = true
	sess.mu.Unlock()

	go s.runTurn(sess, text, reply)
}

// runTurn runs ONE registered turn: deliver the message, forward the engine's
// events as session/update notifications, forward its permission requests to
// the client, relay a session/cancel to the engine, and reply with the stop
// reason when the turn completes.
func (s *Server) runTurn(sess *session, text string, replyWire func(any, *jsonrpc.Error)) {
	// The turn must close BEFORE its response reaches the wire: the client may
	// send the next prompt the instant it reads the reply, and that prompt
	// must not race a deferred reset into "a turn is already in flight".
	reply := func(result any, rerr *jsonrpc.Error) {
		sess.mu.Lock()
		sess.inTurn = false
		sess.mu.Unlock()
		replyWire(result, rerr)
	}

	// Send the message; a dead engine (closed conversation) surfaces on Errs.
	select {
	case sess.engine.In <- agent.ChatMessage{Text: text}:
	case <-sess.cancelTurnCh:
		// Cancelled before the engine even received the message: honor the
		// cancel without running the turn.
		reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
		return
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
				// Conversation ended mid-turn: cancelled/torn down, or died.
				if sess.wasCancelled() {
					reply(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
				} else {
					reply(nil, engineError(<-sess.engine.Errs))
				}
				return
			}
			if ev.Complete != nil {
				reply(api.PromptResponse{StopReason: sess.stopReason(ev.Complete.StopReason)}, nil)
				return
			}
			if ev.Permission != nil {
				// Forward to the editor OFF this loop: session/updates must keep
				// streaming while the human decides.
				go s.forwardPermission(sess, ev.Permission)
				continue
			}
			for _, upd := range sess.mapEvent(ev) {
				if err := s.conn.Notify(api.MethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: upd}); err != nil {
					reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "notify: " + err.Error()})
					return
				}
			}
		case <-sess.cancelTurnCh:
			// Relay the cancel to the engine FROM the turn runner, so it is
			// ordered after the turn's own message; the engine then completes
			// the turn with a cancelled stop reason.
			select {
			case sess.engine.In <- agent.ChatMessage{CancelTurn: true}:
			case <-sess.ctx.Done():
			case <-s.ctx.Done():
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

// forwardPermission relays one engine permission request to the editor as
// session/request_permission and feeds the decision back into the engine. Any
// failure (client error, session teardown) resolves as a dismissed answer —
// the engine then reports a cancelled outcome, neither approving nor
// committing a remembered rejection.
func (s *Server) forwardPermission(sess *session, p *agent.PermissionRequest) {
	answer := agent.PermissionAnswer{ID: p.ID}

	var resp struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionId string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := s.conn.Call(sess.ctx, api.MethodSessionRequestPermission, sess.permissionRequestWire(p), &resp); err != nil {
		clidiag.Warn("ctxloom", "acp agent: permission request failed, dismissing: %v", err)
	} else if resp.Outcome.Outcome == "selected" {
		answer.OptionID = resp.Outcome.OptionId
	}

	select {
	case sess.engine.In <- agent.ChatMessage{Permission: &answer}:
	case <-sess.ctx.Done():
	case <-s.ctx.Done():
	}
}

// handleSetMode switches the session's mode (= a ctxloom profile set: the
// composed defaults, one profile, or a subagent's composed profiles): the
// mode's context is re-assembled and rides the NEXT prompt as a lead block,
// and the client is notified via a current_mode_update. The engine
// conversation itself continues — a mode switch changes the context, not the
// running engine (a subagent mode's engine binding applies only at launch).
func (s *Server) handleSetMode(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req setModeParams
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	sess := s.lookup(req.SessionId)
	if sess == nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown session " + string(req.SessionId)})
		return
	}
	modes := sess.snapshotModes()
	if modes == nil || sess.engine.AssembleMode == nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "session modes not supported for this session"})
		return
	}
	mode, ok := modeByID(modes, req.ModeId)
	if !ok {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown mode " + req.ModeId})
		return
	}

	contextText, err := sess.engine.AssembleMode(sess.ctx, mode)
	if err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "assemble mode: " + err.Error()})
		return
	}

	sess.mu.Lock()
	sess.leadContext = contextText
	sess.contextSent = false
	sess.modes.Current = req.ModeId
	sess.mu.Unlock()

	if err := s.conn.Notify(api.MethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: currentModeUpdateWire(req.ModeId)}); err != nil {
		clidiag.Warn("ctxloom", "acp agent: mode update notify failed: %v", err)
	}
	reply(struct{}{}, nil)
}

// cancelTurn cancels the session's in-flight TURN, per the spec: the prompt
// then resolves with stopReason "cancelled" and the session stays usable for
// the next prompt. Without a turn in flight it is a no-op. The actual relay to
// the engine happens in the turn runner (see runTurn), keeping the read loop
// free and the cancel ordered after the turn's message.
func (s *Server) cancelTurn(id api.SessionId) {
	sess := s.lookup(id)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.inTurn || sess.turnCancelled {
		return
	}
	sess.turnCancelled = true
	select {
	case sess.cancelTurnCh <- struct{}{}:
	default: // buffered(1): the runner already has a pending signal
	}
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
		sess.mu.Lock()
		sess.closed = true
		sess.mu.Unlock()
		sess.cancel()
		sess.engine.Close()
	}
}

// wasCancelled reports whether the in-flight turn was cancelled or the whole
// session torn down — either way the prompt must resolve with "cancelled".
func (sess *session) wasCancelled() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.turnCancelled || sess.closed
}

// stopReason maps the engine's completion onto the ACP stop reason, honoring a
// pending cancel (the spec REQUIRES "cancelled" after session/cancel, even if
// the engine raced the cancel and completed normally).
func (sess *session) stopReason(engineStop string) api.StopReason {
	if sess.wasCancelled() {
		return api.StopReasonCancelled
	}
	return stopReasonToACP(engineStop)
}

// snapshotModes returns the session's mode state under its lock (nil = none).
func (sess *session) snapshotModes() *SessionModes {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.modes == nil {
		return nil
	}
	m := *sess.modes
	return &m
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

// mcpServersFromACP maps the client's session mcpServers onto the engine chat
// request shape (env list → map).
func mcpServersFromACP(servers []api.McpServer) []agent.ChatMCPServer {
	out := make([]agent.ChatMCPServer, 0, len(servers))
	for _, m := range servers {
		var env map[string]string
		if len(m.Env) > 0 {
			env = make(map[string]string, len(m.Env))
			for _, e := range m.Env {
				env[e.Name] = e.Value
			}
		}
		out = append(out, agent.ChatMCPServer{Name: m.Name, Command: m.Command, Args: m.Args, Env: env})
	}
	return out
}

// sessionUpdateParams is the session/update notification body. Update is any
// wire-shaped update value (the SDK's SessionUpdate union or a hand-rolled
// mode update).
type sessionUpdateParams struct {
	SessionId api.SessionId `json:"sessionId"`
	Update    any           `json:"update"`
}
