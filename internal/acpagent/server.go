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
	EngineChat      = operations.EngineChat
	OpenRequest     = operations.OpenRequest
	SessionLLMs     = operations.SessionLLMs
	LLMInfo         = operations.LLMInfo
	SessionModes    = operations.SessionModes
	SessionMode     = operations.SessionMode
	SessionCommands = operations.SessionCommands
	CommandInfo     = operations.CommandInfo
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
	// clientFs is the connected editor's advertised fs capabilities (set once
	// at handleInitialize) — B5 (gap G14), see internal/acpagent/fsupstream.go.
	clientFs api.FileSystemCapabilities

	// editorTerminal records whether the connected editor advertised
	// clientCapabilities.terminal at initialize (B1, gap G6): only then can a
	// session honestly ask its engine's client-role driver to broker
	// terminal/* — an editor that never advertised the capability would be
	// asked to answer a method it cannot. Set exactly once, synchronously,
	// inline in handleInitialize — which the ACP handshake guarantees
	// completes before any session/new can arrive on this connection — so the
	// happens-before edge into the async session-opening goroutines needs no
	// separate synchronization; mu is still taken on both sides purely
	// because it is the cheapest available guard and this field is read from
	// a different goroutine than the one that set it.
	editorTerminal bool
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

	// commands is ctxloom's own command system (B4, gap G5) as advertised to
	// this session — nil when the cwd has no commands configured. Set once
	// at openSession from engine.Commands and never mutated afterward (a
	// mode/profile switch does not change which BUNDLE commands exist), so
	// reads need no lock — see internal/acpagent/commands.go.
	commands *SessionCommands

	// fsUpstream is this session's local fs reach-back listener (B5, gap
	// G14) — nil when the connected editor never declared the fs
	// capability, or listener setup failed (both degrade to local disk,
	// never a refused session). Closed once, at server teardown
	// (closeAllSessions); see internal/acpagent/fsupstream.go.
	fsUpstream *fsUpstreamListener
}

// Serve runs the ACP agent over one reader/writer pair (stdio) until the
// client disconnects or ctx is cancelled. Every session's engine conversation
// is torn down on exit.
func Serve(ctx context.Context, r io.Reader, w io.Writer, open ChatOpener) error {
	s := &Server{open: open, ctx: ctx, sessions: make(map[api.SessionId]*session)}
	s.conn = jsonrpc.NewConn(ctx, r, w, nil, s)
	// s.conn must be FULLY assigned before the read loop can start — Server
	// is its own Handler, and its request handling (emitUpdate) reads
	// s.conn back. Starting the read loop is deferred to Start (see its
	// doc) specifically so this assignment happens-before any dispatch,
	// closing a real (load-only-reproducible) data race. Do not reorder or
	// inline this into the NewConn call above.
	s.conn.Start(ctx)
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
//     an embedded `resource` block's text (embeddedResourceText).
//   - promptCapabilities.image/audio: TRUE as of B2 (gap G3) — handlePrompt
//     now builds a structured agent.ChatMessage.ContentBlocks alongside the
//     flattened Text (see contentBlocksFromACP), so this HUB LAYER no longer
//     discards an image/audio block's bytes: they ride the IR losslessly
//     (Kind/Raw — internal/acp/mapping.go's blockToIR shape) all the way to
//     whichever backend is driving the session. "true" here means exactly
//     what the honesty discipline requires and NO MORE: ctxloom's agent role
//     accepts the block and does something non-silent with it — never a
//     flat, unindicated drop. It does NOT promise every connected engine
//     literally sees pixels/audio: that is a PER-SESSION question the
//     CLIENT-role driver (internal/acp/session.go's buildPromptBlocks)
//     answers against the specific engine's OWN advertised capabilities,
//     degrading to a visible flatten-WITH-warning placeholder when the
//     engine can't take it — never a silent drop at that layer either. A
//     backend that only reads ChatMessage.Text (claude/codex/kiro/opencode's
//     own native StructuredChat drivers, untouched by this slice) still gets
//     a non-silent placeholder too: promptText's image/audio case now emits
//     a labeled marker instead of omitting the block outright.
//   - mcpCapabilities: http/sse true, acp false (B3, gap G11) — ctxloom now
//     accepts an editor-supplied http/sse MCP server (mcpServersFromACP) and
//     carries it onward through the chosen engine's own ACP client
//     (internal/acp/session.go's mcpServersToACP), gated per-session on
//     THAT engine's own advertised capability — a downstream engine that
//     cannot take one gets a LOUD ChatSessionInfo.MCPServers status, never a
//     silent drop (see mcpServersToACP's doc comment). acp (the unstable
//     ACP-transport variant) stays false: it names an ACP-side component
//     ctxloom has no seam for yet, so an entry of that shape is still
//     rejected (loudly — see mcpServersFromACP) rather than forwarded blind.
//     McpCapabilities has no "stdio" flag because stdio is the protocol's
//     unconditional baseline.
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
	// B1 (gap G6): capture whether THIS editor advertised
	// clientCapabilities.terminal — read by openSession (via
	// editorAdvertisedTerminal) once a session opens its engine conversation,
	// so that conversation's own client-role driver can honestly advertise
	// (or not) Terminal to whichever engine it drives. There is no
	// "terminal capability" for THIS agent-role response to advertise back
	// (ACP's AgentCapabilities has no such field — terminal is a client→agent
	// capability only), so relaying it downstream to the engine is the whole
	// of B1's agent-role half.
	s.mu.Lock()
	s.editorTerminal = req.ClientCapabilities.Terminal
	s.mu.Unlock()
	negotiated := req.ProtocolVersion
	if negotiated > api.ProtocolVersionNumber {
		// min(clientVersion, ours): we cannot speak a version newer than the
		// one this SDK vendors.
		negotiated = api.ProtocolVersionNumber
	}
	// B5 (gap G14): stash the CONNECTED EDITOR's own fs capabilities —
	// startFsUpstream (internal/acpagent/fsupstream.go) reads this to decide
	// whether offering host-axis fs chaining is even possible for this
	// connection at all.
	s.setClientFs(req.ClientCapabilities.Fs)
	reply(api.InitializeResponse{
		ProtocolVersion: negotiated,
		AgentCapabilities: api.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: api.PromptCapabilities{
				EmbeddedContext: true,
				Image:           true,
				Audio:           true,
			},
			McpCapabilities: api.McpCapabilities{
				Http: true,
				Sse:  true,
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
	// B5 (gap G14): openSessionWithFsUpstream (not openSession directly)
	// stands up this session's fs reach-back listener FIRST — its address
	// must ride the OpenRequest so OpenEngineSession can decide, per the
	// resolved axes, whether to actually forward it to the engine.
	sess, rerr := s.openSessionWithFsUpstream(OpenRequest{Cwd: req.Cwd, MCPServers: mcpServersFromACP(req.McpServers), ForwardTerminal: s.editorAdvertisedTerminal()}, "")
	if rerr != nil {
		reply(nil, rerr)
		return
	}
	// ISO3/ISO4: the session initialization summary, sent BEFORE the reply
	// — and therefore before the editor can possibly send session/prompt —
	// so isolation posture, model/profiles/fragments, and commands/skills/
	// MCP status are all known at connect, not gated behind a turn. See
	// announce.go's emitSessionInitSummary doc for why this can't ride the
	// engine's Events channel instead.
	if rerr := s.emitSessionInitSummary(sess); rerr != nil {
		reply(nil, rerr)
		return
	}
	// B4 (gap G5): available_commands_update has no field on session/new's
	// response (unlike modes/models) — the spec requires it as a
	// session/update notification instead. Sent before the reply so a
	// client's command palette is populated from the earliest possible
	// moment; emitUpdate is a no-op when the session has no commands.
	if rerr := s.emitAvailableCommands(sess); rerr != nil {
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
	// B5 (gap G14): see handleSessionNew's identical call for why this goes
	// through openSessionWithFsUpstream.
	sess, rerr := s.openSessionWithFsUpstream(OpenRequest{
		Cwd:             req.Cwd,
		MCPServers:      mcpServersFromACP(req.McpServers),
		ResumeHarp:      string(req.SessionId),
		ForwardTerminal: s.editorAdvertisedTerminal(),
	}, req.SessionId)
	if rerr != nil {
		reply(nil, rerr)
		return
	}
	// ISO3/ISO4: see handleSessionNew's identical call — the resumed
	// session's (freshly re-dialed engine's) initialization summary is sent
	// before anything else, including the history replay just below: this
	// run's summary is current-session information, not part of the
	// recorded past.
	if rerr := s.emitSessionInitSummary(sess); rerr != nil {
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
	// B4 (gap G5): see handleSessionNew's identical call for why this rides a
	// notification rather than a loadSessionResult field.
	if rerr := s.emitAvailableCommands(sess); rerr != nil {
		reply(nil, rerr)
		return
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
		commands:     engine.Commands,
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
	// B2 (gap G3): carry the prompt's content blocks through STRUCTURALLY too
	// (agent.ChatMessage.ContentBlocks, IR2's carrier) — text alone loses
	// image/audio entirely and only ever inlines resource/resource_link. A
	// backend that consumes ContentBlocks (today: internal/acp's CLIENT-role
	// driver) gets the full-fidelity blocks to deliver (capability-gated
	// there); every other backend still only reads Text, unchanged.
	blocks := contentBlocksFromACP(req.Prompt)

	// B4 (gap G5): a prompt beginning "/<name>" invoking one of ctxloom's OWN
	// advertised commands (see sess.commands) is expanded to that command's
	// content before it ever reaches the engine — the engine has no idea
	// what ctxloom's bundle commands are. Text that doesn't match a known
	// command name (most "/word..." messages are just user text) passes
	// through byte-for-byte unchanged; see expandCommand's doc for why this
	// must never be a fuzzy match.
	//
	// PARITY WITH blocks (B2/B4 merge invariant — do not drop this): a naive
	// keep-both merge of B2 and B4 would expand `text` here but leave
	// `blocks` carrying the RAW, unexpanded "/name ..." — a ContentBlocks
	// consumer (internal/acp's CLIENT-role driver, which is the path B2
	// exists to enable) would then run the engine on the UNEXPANDED
	// invocation while a text-only backend got the expansion, silently. When
	// expandCommand actually matched (matched == true), blocks is rebuilt the
	// same way the lead-context prefix below rebuilds it: expandedCommandBlocks
	// drops the original text-kind block(s) (the raw invocation) and replaces
	// them with ONE new text block carrying the identical expanded string,
	// preserving every non-text block (image/audio/resource/resource_link)
	// untouched — so an image attached alongside "/code-review" still reaches
	// the engine. An unmatched prompt (matched == false) leaves blocks
	// completely untouched, in both forms.
	expanded, matched, cerr := expandCommand(sess.ctx, sess.commands, text)
	if cerr != nil {
		reply(nil, cerr)
		return
	}
	if matched {
		text = expanded
		blocks = expandedCommandBlocks(blocks, expanded)
	}

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
		// delivery model, no engine flags needed. The structured form gets
		// the SAME prefix, as its own leading text block, so a ContentBlocks
		// consumer sees the identical lead context a Text-only consumer does.
		text = sess.leadContext + "\n\n" + text
		blocks = append([]agent.ContentBlock{{Kind: "text", Text: sess.leadContext}}, blocks...)
	}
	sess.contextSent = true
	sess.mu.Unlock()

	go s.runTurn(sess, text, blocks, reply)
}

// runTurn runs ONE registered turn: deliver the message, forward the engine's
// events as session/update notifications, forward its permission requests to
// the client, relay a session/cancel to the engine, and reply with the stop
// reason when the turn completes.
func (s *Server) runTurn(sess *session, text string, blocks []agent.ContentBlock, replyWire func(any, *jsonrpc.Error)) {
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
	case sess.engine.In <- agent.ChatMessage{Text: text, ContentBlocks: blocks}:
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
			if ev.Terminal != nil {
				// B1 (gap G6): forward to the editor OFF this loop, exactly
				// like a permission request — session/updates must keep
				// streaming while the editor answers a terminal/* call.
				go s.forwardTerminal(sess, ev.Terminal)
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

// editorAdvertisedTerminal reports whether the connected editor advertised
// clientCapabilities.terminal at initialize (captured once in
// handleInitialize). read under mu for cross-goroutine safety with the
// synchronous write there.
func (s *Server) editorAdvertisedTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editorTerminal
}

// forwardTerminal relays one engine terminal/* request (B1, gap G6) to the
// connected editor and feeds the answer back into the engine. ctxloom
// implements NO terminal of its own here — it substitutes THIS session's
// real editor-facing id (sess.id) into the request before relaying (the
// engine's own opaque request arrived with that field already stripped —
// see internal/acp/session.go's stripSessionID), exactly as forwardPermission
// substitutes sess.id for a forwarded permission request instead of carrying
// the engine's own id through. Any failure (unknown op, decode error,
// transport error, session teardown) resolves as a TerminalResponse.Error,
// never a silent drop: the engine's own terminal/create (etc.) caller must
// see a real answer, not a hang or a fabricated success — this codebase's
// signature failure mode is exit 0 with zero effect, and this path exists
// specifically to not repeat it.
func (s *Server) forwardTerminal(sess *session, p *agent.TerminalRequest) {
	answer := agent.TerminalResponse{ID: p.ID}

	method, merr := terminalClientMethod(p.Op)
	if merr != nil {
		answer.Error = merr.Error()
	} else {
		wireParams, werr := terminalParamsWithSession(p.Params, sess.id)
		if werr != nil {
			answer.Error = werr.Error()
		} else {
			var result json.RawMessage
			if cerr := s.conn.Call(sess.ctx, method, wireParams, &result); cerr != nil {
				answer.Error = cerr.Error()
			} else {
				answer.Result = result
			}
		}
	}

	select {
	case sess.engine.In <- agent.ChatMessage{Terminal: &answer}:
	case <-sess.ctx.Done():
	case <-s.ctx.Done():
	}
}

// terminalClientMethod maps the agent.TerminalOp* vocabulary onto the ACP
// terminal/* JSON-RPC method name to call on the editor. An unrecognized op
// is a defect in whatever produced the TerminalRequest (today: only
// internal/acp/session.go's terminalOp, which only ever emits these five) —
// reported as an error, never silently ignored or guessed at.
func terminalClientMethod(op string) (string, error) {
	switch op {
	case agent.TerminalOpCreate:
		return api.ClientMethodTerminalCreate, nil
	case agent.TerminalOpOutput:
		return api.ClientMethodTerminalOutput, nil
	case agent.TerminalOpWaitForExit:
		return api.ClientMethodTerminalWaitForExit, nil
	case agent.TerminalOpKill:
		return api.ClientMethodTerminalKill, nil
	case agent.TerminalOpRelease:
		return api.ClientMethodTerminalRelease, nil
	default:
		return "", fmt.Errorf("ctxloom acp: unknown terminal operation %q", op)
	}
}

// terminalParamsWithSession re-injects THIS session's real editor-facing id
// into a forwarded terminal/* request's params (stripped upstream — see
// internal/acp/session.go's stripSessionID) so the outbound call carries a
// sessionId the connected editor actually recognizes.
func terminalParamsWithSession(params json.RawMessage, sessID api.SessionId) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &m); err != nil {
			return nil, fmt.Errorf("decode terminal request params: %w", err)
		}
	}
	sidJSON, err := json.Marshal(sessID)
	if err != nil {
		return nil, err
	}
	m["sessionId"] = sidJSON
	return json.Marshal(m)
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
		// B5 (gap G14): tear down this session's fs reach-back listener, if
		// one was ever stood up — nil-safe (Close checks for a nil receiver).
		if err := sess.fsUpstream.Close(); err != nil {
			clidiag.Warn("ctxloom", "acp agent: fs-upstream listener cleanup: %v", err)
		}
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

// promptText flattens a prompt's content blocks to text for a backend that
// only ever consumes text (every native backend but internal/acp's — claude/
// codex/kiro/opencode's own StructuredChat drivers, untouched by this slice).
// Text blocks pass through; `resource` blocks inline their embedded
// resource's text; `resource_link` blocks become a labeled reference line —
// so "add context" content reaches the engine instead of vanishing.
//
// B2 (gap G3): image/audio blocks, and a `resource` block carrying only a
// binary blob (no text), have no text projection — but they are no longer
// SILENTLY dropped here: each renders a labeled placeholder line (kind, mime
// type, byte count) so a text-only backend's model at least sees that
// something arrived, instead of the turn quietly missing it (this
// codebase's signature failure mode — exit 0, success, zero bytes
// delivered). A richer backend gets the REAL bytes via
// agent.ChatMessage.ContentBlocks (contentBlocksFromACP), not this flattened
// projection — see handlePrompt.
//
// ContentBlock carries no discriminator field in the fork's generated shape —
// dispatch switches on which variant pointer is non-nil.
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
			} else {
				parts = append(parts, binaryResourcePlaceholder(b.Resource))
			}
		case b.ResourceLink != nil:
			if s := resourceLinkText(b.ResourceLink); s != "" {
				parts = append(parts, s)
			}
		case b.Image != nil:
			parts = append(parts, mediaPlaceholderLine("image", b.Image.MimeType, len(b.Image.Data)))
		case b.Audio != nil:
			parts = append(parts, mediaPlaceholderLine("audio", b.Audio.MimeType, len(b.Audio.Data)))
		}
	}
	return strings.Join(parts, "\n")
}

// mediaPlaceholderLine renders the visible, non-silent placeholder for an
// image/audio block flattened to plain text (see promptText's B2 note).
func mediaPlaceholderLine(kind, mimeType string, dataLen int) string {
	return fmt.Sprintf("[%s content received (mimeType=%s, %d bytes) — this flattened text channel cannot render it; a structured-content-aware backend receives it via ContentBlocks instead]", kind, mimeType, dataLen)
}

// binaryResourcePlaceholder renders the visible placeholder for an embedded
// `resource` block carrying only a binary blob (embeddedResourceText returns
// "" for one) — B2's fix for the pre-existing silent drop this path used to
// have (see TestPromptText_ResourceBlocks's history: a blob resource used to
// vanish with no trace at all).
func binaryResourcePlaceholder(r *api.ContentBlockResource) string {
	if r == nil || r.Resource.BlobResourceContents == nil {
		return ""
	}
	b := r.Resource.BlobResourceContents
	mimeType := ""
	if b.MimeType != nil {
		mimeType = *b.MimeType
	}
	return fmt.Sprintf("[binary resource %s received (mimeType=%s, %d bytes) — this flattened text channel cannot render it; a structured-content-aware backend receives it via ContentBlocks instead]", b.Uri, mimeType, len(b.Blob))
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

// contentBlocksFromACP projects a session/prompt's content blocks onto the
// IR2 structured form (agent.ContentBlock: Kind/Text/Raw) — the AGENT-role
// mirror of internal/acp/mapping.go's blockToIR (CLIENT-role intake from an
// ENGINE's own updates). Every variant's FULL bytes ride in Raw regardless of
// kind, so image/audio/resource are carried losslessly all the way to a
// backend that can act on them (B2, gap G3) — this is what makes
// handleInitialize's promptCapabilities.image/audio: true honest: this layer
// never drops the bytes, whatever a specific downstream engine later decides
// to do with them (internal/acp/session.go's buildPromptBlocks degrades that
// per-session, never silently).
func contentBlocksFromACP(blocks []api.ContentBlock) []agent.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]agent.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		kind, text := "", ""
		switch {
		case b.Text != nil:
			kind, text = "text", b.Text.Text
		case b.Image != nil:
			kind = "image"
		case b.Audio != nil:
			kind = "audio"
		case b.ResourceLink != nil:
			kind = "resource_link"
		case b.Resource != nil:
			kind = "resource"
		default:
			continue // no recognized variant — nothing to carry
		}
		raw, err := json.Marshal(b)
		if err != nil {
			continue // never expected: b was itself just decoded from JSON
		}
		out = append(out, agent.ContentBlock{Kind: kind, Text: text, Raw: raw})
	}
	return out
}

// mcpServersFromACP maps the client's session mcpServers onto the engine chat
// request shape (env/header list → map). Stdio is the protocol's
// unconditional baseline, always accepted. Http/Sse (B3, gap G11) are now
// accepted too — ctxloom's own initialize advertises both true (see
// handleInitialize) — and carried onward as agent.ChatMCPServer with
// Transport/URL/Headers set; whether the SESSION'S chosen engine can
// actually use them is a separate, per-session question answered by
// internal/acp/session.go's mcpServersToACP (which gates on that engine's
// own advertised capability and reports a loud status when it can't).
//
// The ACP-transport variant (m.Acp, still UNSTABLE in the spec) names an
// ACP-side component ctxloom has no seam to reach yet — accepting it would
// be a lie (it would never actually connect), so it is REJECTED loudly
// rather than silently dropped, same as an entry with no variant set at all
// (a malformed frame a conforming client should never send).
func mcpServersFromACP(servers []api.McpServer) []agent.ChatMCPServer {
	out := make([]agent.ChatMCPServer, 0, len(servers))
	for _, m := range servers {
		switch {
		case m.Stdio != nil:
			var env map[string]string
			if len(m.Stdio.Env) > 0 {
				env = make(map[string]string, len(m.Stdio.Env))
				for _, e := range m.Stdio.Env {
					env[e.Name] = e.Value
				}
			}
			out = append(out, agent.ChatMCPServer{Name: m.Stdio.Name, Command: m.Stdio.Command, Args: m.Stdio.Args, Env: env})
		case m.Http != nil:
			out = append(out, agent.ChatMCPServer{
				Name: m.Http.Name, Transport: agent.MCPTransportHTTP, URL: m.Http.Url,
				Headers: httpHeadersToMap(m.Http.Headers),
			})
		case m.Sse != nil:
			out = append(out, agent.ChatMCPServer{
				Name: m.Sse.Name, Transport: agent.MCPTransportSSE, URL: m.Sse.Url,
				Headers: httpHeadersToMap(m.Sse.Headers),
			})
		case m.Acp != nil:
			clidiag.Warn("ctxloom", "acp agent: session/new mcpServers: %q is an ACP-transport server (McpServer::Acp) — ctxloom has no seam to reach an ACP-side MCP component yet; dropping it rather than forwarding a server that would never connect", m.Acp.Name)
		default:
			clidiag.Warn("ctxloom", "acp agent: session/new mcpServers: an entry set no known transport variant (stdio/http/sse/acp); dropping it")
		}
	}
	return out
}

// httpHeadersToMap converts the ACP wire's HTTP header list to the map shape
// agent.ChatMCPServer.Headers carries. nil for an empty list.
func httpHeadersToMap(headers []api.HttpHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		out[h.Name] = h.Value
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
