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
// (ctxloom profile sets — the composed defaults, each profile, each agent's
// composed set — surfaced as ACP session modes; a switch re-assembles the
// lead context for the next turn), session/cancel (cancels the in-flight TURN;
// the session stays usable and the prompt resolves with stopReason
// "cancelled"). Engine permission requests forward to the connected editor as
// session/request_permission — real interactive approvals in structured mode.
package acpagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// agentName/agentVersion identify ctxloom in the initialize response.
const (
	agentName    = "ctxloom"
	agentVersion = "1.0.0"
)

// protocolFloor is the LOWEST ACP protocol version this agent will negotiate.
// The spec renumbered its versioning scheme from fractional (0.14.0) to
// integer (v1.0.0) on 2026-06-24; schema-v1.19.0 (what api.ProtocolVersionNumber
// pins) is entirely inside the integer v1.x scheme, and there is no lower
// integer version whose wire shapes this codebase understands — floor and
// ceiling coincide today, both api.ProtocolVersionNumber (1). A client below
// this floor is refused (see handleInitialize), not silently answered as if
// compatible.
const protocolFloor = api.ProtocolVersion(api.ProtocolVersionNumber)

// EngineChat, OpenRequest, and the session-modes/LLM-advertisement shapes
// they carry are frontend-neutral (ISO0): they live in internal/operations
// (engine_types.go) so internal/operations.OpenEngineSession can produce them
// without this package depending back on any one frontend's wire protocol.
// These are type ALIASES, not new types — every existing acpagent.X reference
// in this package (and any external caller) keeps compiling unchanged.
type (
	EngineChat   = operations.EngineChat
	OpenRequest  = operations.OpenRequest
	SessionLLMs  = operations.SessionLLMs
	LLMInfo      = operations.LLMInfo
	SessionModes = operations.SessionModes
	SessionMode  = operations.SessionMode
)

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
	case api.AgentMethodInitialize:
		s.handleInitialize(params, reply)
	case api.AgentMethodAuthenticate:
		s.handleAuthenticate(params, reply)
	case api.AgentMethodLogout:
		s.handleLogout(params, reply)
	case api.AgentMethodSessionNew:
		go s.handleSessionNew(params, reply)
	case api.AgentMethodSessionLoad:
		go s.handleSessionLoad(params, reply)
	case api.AgentMethodSessionPrompt:
		s.handlePrompt(params, reply)
	case api.AgentMethodSessionSetMode:
		go s.handleSetMode(params, reply)
	case api.AgentMethodSessionSetConfigOption:
		go s.handleSetConfigOption(params, reply)
	case api.AgentMethodSessionDelete:
		s.handleSessionDelete(params, reply)
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ctxloom acp: method not supported: " + method})
	}
}

// handleInitialize answers the ACP handshake: negotiates a protocol version
// (min(clientVersion, ours); a client below protocolFloor is refused rather
// than silently answered as if compatible — version mismatches must be LOUD,
// never undetected) and advertises ctxloom's agent capabilities TRUTHFULLY
// for what THIS build actually does today, never a later slice's work:
//
//   - loadSession: true — session/load is implemented (handleSessionLoad).
//   - promptCapabilities.embeddedContext: true — promptText already inlines
//     an embedded `resource` block's text (embeddedResourceText). image/audio:
//     left false — those ContentBlock variants have no text projection in
//     promptText today and are silently dropped; claiming true would be
//     exactly the lie this codebase must not tell (an advertised capability
//     that quietly discards the content it claims to accept). Multimodal
//     intake is a later slice (B2), not this one.
//   - mcpCapabilities: left at its zero value (acp/http/sse all false) —
//     ctxloom only ever forwards STDIO MCP servers (mcpServersFromACP; an
//     http/sse/acp entry is silently skipped there, unchanged by this
//     slice). McpCapabilities has no "stdio" flag because stdio is the
//     protocol's unconditional baseline, so leaving the rest false is the
//     complete and honest claim — HTTP/SSE passthrough is a later slice (B3).
//   - sessionCapabilities: left at its zero value — no session/close,
//     /delete, /fork, /list, /resume, or /additionalDirectories exist yet
//     (handleSessionDelete already answers a probe of the one of these a
//     client might try honestly).
//   - authMethods: [] — ctxloom needs no authentication today. authenticate
//     and logout still EXIST as recognized methods and answer per spec (see
//     handleAuthenticate/handleLogout) rather than falling through to the
//     generic method-not-found default case below.
//
// This is also the one true SUPERSET a multi-engine hub can honestly
// advertise at initialize time: the editor picks WHICH engine to actually
// run per-session at session/new, long after this handshake completes, so
// no per-engine capability can be promised here. Where a session's engine
// cannot honor something this response implies (e.g. it can't load a
// session, or its own image/audio/MCP support differs), degradation must
// happen per-session and LOUDLY — reject or flatten the unsupported part
// rather than silently dropping it — never invisibly here.
func (s *Server) handleInitialize(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.InitializeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	if req.ProtocolVersion < protocolFloor {
		reply(nil, &jsonrpc.Error{
			Code: jsonrpc.CodeInvalidParams,
			Message: fmt.Sprintf(
				"ctxloom acp: unsupported protocolVersion %d: ctxloom speaks ACP protocol version %d (schema-v1.19.0) and nothing earlier — refusing the connection rather than negotiating a version it cannot correctly speak",
				req.ProtocolVersion, api.ProtocolVersionNumber,
			),
		})
		return
	}
	negotiated := req.ProtocolVersion
	if negotiated > api.ProtocolVersionNumber {
		// min(clientVersion, ours): we cannot speak a version newer than the
		// one this SDK vendors.
		negotiated = api.ProtocolVersionNumber
	}
	reply(api.InitializeResponse{
		ProtocolVersion: negotiated,
		AgentCapabilities: api.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: api.PromptCapabilities{
				EmbeddedContext: true,
			},
		},
		AuthMethods: []api.AuthMethod{},
		AgentInfo:   &api.Implementation{Name: agentName, Version: agentVersion},
	}, nil)
}

// handleAuthenticate answers the authenticate method honestly. authenticate
// is a CORE method on the ACP Agent interface (unlike capability-gated
// methods such as session/delete) — it must exist and answer, never
// method-not-found. ctxloom advertises authMethods: [] at initialize (see
// handleInitialize), so ANY methodId a conforming client could have gotten
// from OUR own initialize response does not exist; a request naming one is
// therefore invalid, and is answered as such — a clean, specific error, not
// the generic "method not supported" a truly-unrecognized method would get.
func (s *Server) handleAuthenticate(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.AuthenticateRequest
	_ = json.Unmarshal(params, &req) // best-effort: only used to name the method in the error
	reply(nil, &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: "ctxloom acp: authenticate: no auth methods are configured — ctxloom requires no authentication (initialize advertises authMethods: []); method " + string(req.MethodId) + " is not available",
	})
}

// handleLogout answers the logout method honestly. Like authenticate, logout
// is a CORE Agent method and must exist and answer, never method-not-found.
// ctxloom never advertises the auth.logout capability (see handleInitialize)
// and never authenticates a session in the first place, so there is no
// authenticated state for this call to end — answered as a clean, specific
// error distinct from authenticate's, so a client can tell the two apart.
func (s *Server) handleLogout(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	reply(nil, &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: "ctxloom acp: logout: ctxloom does not support authentication (agentCapabilities.auth.logout is not advertised); there is no authenticated session to end",
	})
}

// handleSessionDelete answers session/delete honestly: ctxloom's agent role
// does not support deleting a recorded session (there is no session store
// this side can remove entries from — a ctxloom "session" is a live engine
// conversation plus, when accounting is available, a recorded harp on disk
// that other ctxloom surfaces (recover, session history) still expect to
// find). session/delete graduated unstable→stable in schema-v1.19.0 onto the
// CORE Agent interface, but it stays capability-gated
// (sessionCapabilities.delete) — this agent never advertises that capability
// (see HandleRequest's initialize response), so a spec-conforming client
// should never call it. A client that calls it anyway (or one probing
// blind) gets an honest METHOD-NOT-FOUND error naming exactly what is
// unsupported, never a silent success that pretends to have deleted
// something — that is this codebase's characteristic failure mode (exit 0,
// zero effect, no error) and this handler exists specifically to not repeat
// it.
func (s *Server) handleSessionDelete(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.DeleteSessionRequest
	_ = json.Unmarshal(params, &req) // best-effort: only used to name the session in the error
	reply(nil, &jsonrpc.Error{
		Code:    jsonrpc.CodeMethodNotFound,
		Message: "ctxloom acp: session/delete not supported: ctxloom does not support deleting recorded sessions (session " + string(req.SessionId) + ")",
	})
}

// HandleNotification handles session/cancel; anything else is dropped with a
// warning (never crash the connection on an unmodeled frame).
func (s *Server) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	if method != api.AgentMethodSessionCancel {
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
	modes := sess.snapshotModes()
	reply(newSessionResult{
		SessionId:     sess.id,
		Modes:         modeStateWire(modes),
		Models:        modelStateWire(sess.engine.LLMs),
		ConfigOptions: configOptionsWire(modes, sess.engine.LLMs),
	}, nil)
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
			if err := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: upd}); err != nil {
				reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "replay: " + err.Error()})
				return
			}
		}
	}
	modes := sess.snapshotModes()
	reply(loadSessionResult{
		Modes:         modeStateWire(modes),
		Models:        modelStateWire(sess.engine.LLMs),
		ConfigOptions: configOptionsWire(modes, sess.engine.LLMs),
	}, nil)
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
	if engine.WatchChildren != nil {
		go s.pushChildUpdates(sess)
	}
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
			if ev.Session != nil {
				// One-time session metadata (model/mcp): surface it as a
				// session_info_update so a client can render a model header.
				if rerr := s.emitUpdate(sess, sessionInfoUpdateWire(ev.Session)); rerr != nil {
					reply(nil, rerr)
					return
				}
				continue
			}
			if ev.Complete != nil {
				// The turn's accounting rides ahead of the completion as a
				// usage_update (context gauge + cost), then the turn ends.
				if rerr := s.emitUpdate(sess, usageUpdateWire(ev.Complete)); rerr != nil {
					reply(nil, rerr)
					return
				}
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
				if rerr := s.emitUpdate(sess, upd); rerr != nil {
					reply(nil, rerr)
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
	if err := s.conn.Call(sess.ctx, api.ClientMethodSessionRequestPermission, sess.permissionRequestWire(p), &resp); err != nil {
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
// composed defaults, one profile, or an agent's composed profiles): the
// mode's context is re-assembled and rides the NEXT prompt as a lead block,
// and the client is notified via a current_mode_update. The engine
// conversation itself continues — a mode switch changes the context, not the
// running engine (an agent mode's engine binding applies only at launch).
func (s *Server) handleSetMode(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.SetSessionModeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	sess := s.lookup(req.SessionId)
	if sess == nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown session " + string(req.SessionId)})
		return
	}
	if rerr := s.switchProfile(sess, string(req.ModeId)); rerr != nil {
		reply(nil, rerr)
		return
	}
	reply(api.SetSessionModeResponse{}, nil)
}

// switchProfile is the ONE mechanism behind BOTH session/set_mode and
// session/set_config_option's "profile" option (CO1: profile is the
// spec-general home for exactly what modes was repurposed to do — swapping
// ctxloom's assembled context/persona mid-session; see internal/acpagent's
// package doc). It reassembles the lead context for the next turn and
// notifies BOTH surfaces — a current_mode_update AND a configOptionUpdate —
// regardless of which method the client used to trigger the switch, per
// CO1's COMPAT decision: modes/models emit alongside set_config_option
// through a transitional window, never replaced by it. Returns a
// jsonrpc.Error on failure (session modes unsupported, unknown mode,
// assembly failure); nil on success, with sess already mutated.
func (s *Server) switchProfile(sess *session, modeID string) *jsonrpc.Error {
	modes := sess.snapshotModes()
	if modes == nil || sess.engine.AssembleMode == nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "session modes not supported for this session"}
	}
	mode, ok := modeByID(modes, modeID)
	if !ok {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown mode " + modeID}
	}

	contextText, err := sess.engine.AssembleMode(sess.ctx, mode)
	if err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "assemble mode: " + err.Error()}
	}

	sess.mu.Lock()
	sess.leadContext = contextText
	sess.contextSent = false
	sess.modes.Current = modeID
	updatedModes := *sess.modes
	sess.mu.Unlock()

	if nerr := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: currentModeUpdateWire(modeID)}); nerr != nil {
		clidiag.Warn("ctxloom", "acp agent: mode update notify failed: %v", nerr)
	}
	opts := configOptionsWire(&updatedModes, sess.engine.LLMs)
	if nerr := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: configOptionUpdateWire(opts)}); nerr != nil {
		clidiag.Warn("ctxloom", "acp agent: config option update notify failed: %v", nerr)
	}
	return nil
}

// handleSetConfigOption implements session/set_config_option (CO1): the
// spec's generalization of model/mode selection (schema-v1.19.0) onto
// ctxloom's session-mutable surface. "profile" shares switchProfile's real,
// working mechanism with session/set_mode (COMPAT: both fire on either
// trigger). "model" is deliberately refused rather than silently accepted:
// the requested model IS honored now (see internal/claude/chat.go's
// claudeModelSelectionQuirk, the session-START fix for the live billing bug
// this slice exists for), but a LIVE mid-session change has no channel yet —
// the running engine conversation lives in a SEPARATE process (the
// self-invoking "ctxloom llm serve" runner), reached only through
// agent.ChatMessage/ChatEvent's fixed protobuf oneofs, neither of which
// carries a model-change variant today. Silently accepting and doing
// nothing would be exactly this codebase's characteristic failure mode
// (exit 0, zero effect) — see handleSessionDelete for the same honest
// method-not-found precedent.
func (s *Server) handleSetConfigOption(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.SetSessionConfigOptionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	var sessionID api.SessionId
	var configID api.SessionConfigId
	switch {
	case req.ValueId != nil:
		sessionID, configID = req.ValueId.SessionId, req.ValueId.ConfigId
	case req.Boolean != nil:
		sessionID, configID = req.Boolean.SessionId, req.Boolean.ConfigId
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "session/set_config_option: no value variant set"})
		return
	}
	sess := s.lookup(sessionID)
	if sess == nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown session " + string(sessionID)})
		return
	}

	switch configID {
	case profileConfigID:
		if req.ValueId == nil {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "ctxloom acp: profile is a select option (needs a value id, not a boolean)"})
			return
		}
		if rerr := s.switchProfile(sess, string(req.ValueId.Value)); rerr != nil {
			reply(nil, rerr)
			return
		}
		reply(api.SetSessionConfigOptionResponse{ConfigOptions: configOptionsWire(sess.snapshotModes(), sess.engine.LLMs)}, nil)
	case modelConfigID:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ctxloom acp: live model switching is not implemented yet (the requested model IS honored at session start); open a new session to run under a different model"})
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "ctxloom acp: unknown config option " + string(configID)})
	}
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

// emitUpdate sends one session/update notification for the session, returning a
// JSON-RPC error when the transport fails (so the caller can fail the turn). A
// nil update is a no-op — the wire projections return nil when there is nothing
// worth surfacing.
func (s *Server) emitUpdate(sess *session, update any) *jsonrpc.Error {
	if update == nil {
		return nil
	}
	if err := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: update}); err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "notify: " + err.Error()}
	}
	return nil
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

// promptText flattens a prompt's content blocks to text for the engine (which
// consumes text). Text blocks pass through; `resource` blocks inline their
// embedded resource's text; `resource_link` blocks become a labeled reference
// line — so "add context" content reaches the engine instead of vanishing.
// Binary/opaque blocks (images, audio, blob resources) have no text projection
// and are still dropped. ContentBlock carries no discriminator field in the
// fork's generated shape — dispatch switches on which variant pointer is
// non-nil.
func promptText(blocks []api.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			if b.Text.Text != "" {
				parts = append(parts, b.Text.Text)
			}
		case b.Resource != nil:
			if s := embeddedResourceText(b.Resource); s != "" {
				parts = append(parts, s)
			}
		case b.ResourceLink != nil:
			if s := resourceLinkText(b.ResourceLink); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// embeddedResourceText inlines an embedded `resource` block's text. ACP embeds
// either a text resource ({uri,text,mimeType}) or a binary blob
// ({uri,blob,mimeType}); only text is inlinable, so a blob yields "" (dropped).
// A uri, when present, prefixes the text as a label. The embedded resource is
// now a PROPERLY TYPED union (TextResourceContents/BlobResourceContents) — the
// pinned SDK left this interface{} (decoded as a raw map), requiring a type
// assertion this function used to do.
func embeddedResourceText(r *api.ContentBlockResource) string {
	if r == nil || r.Resource.TextResourceContents == nil {
		return ""
	}
	t := r.Resource.TextResourceContents
	if t.Text == "" {
		return ""
	}
	if t.Uri != "" {
		return t.Uri + ":\n" + t.Text
	}
	return t.Text
}

// resourceLinkText renders a `resource_link` block as one labeled reference
// line, so a referenced resource reaches the engine as a pointer it can act on
// rather than being dropped. A link with no uri has nothing to reference.
// Title/Description are now PROPERLY TYPED as *string (see
// the pinned SDK's unions_generated.go ContentBlockResourceLink) rather than the
// interface{} the pinned SDK's union file left them as.
func resourceLinkText(l *api.ContentBlockResourceLink) string {
	if l == nil || l.Uri == "" {
		return ""
	}
	label := l.Name
	if l.Title != nil && *l.Title != "" {
		label = *l.Title
	}
	line := "[resource: "
	if label != "" {
		line += label + " — "
	}
	line += l.Uri + "]"
	if l.Description != nil && *l.Description != "" {
		line += " " + *l.Description
	}
	return line
}

// mcpServersFromACP maps the client's session mcpServers onto the engine chat
// request shape (env list → map). ctxloom only ever spawns stdio MCP servers
// (HTTP/SSE/ACP-transport entries are out of scope and silently skipped, same
// as before this SDK swap — the fork's McpServer is now a discriminated union
// of http/sse/acp/stdio, unlike the flat stdio-only struct it replaces).
func mcpServersFromACP(servers []api.McpServer) []agent.ChatMCPServer {
	out := make([]agent.ChatMCPServer, 0, len(servers))
	for _, m := range servers {
		if m.Stdio == nil {
			continue
		}
		var env map[string]string
		if len(m.Stdio.Env) > 0 {
			env = make(map[string]string, len(m.Stdio.Env))
			for _, e := range m.Stdio.Env {
				env[e.Name] = e.Value
			}
		}
		out = append(out, agent.ChatMCPServer{Name: m.Stdio.Name, Command: m.Stdio.Command, Args: m.Stdio.Args, Env: env})
	}
	return out
}

// sessionUpdateParams is the session/update notification body. Update is any
// wire-shaped update value (an api.SessionUpdate value, or ctxloom's own
// session-info extension — see ctxloomSessionInfoUpdate).
type sessionUpdateParams struct {
	SessionId api.SessionId `json:"sessionId"`
	Update    any           `json:"update"`
}
