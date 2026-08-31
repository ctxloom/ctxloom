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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/coder/acp-go-sdk"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// agentName identifies ctxloom in the initialize response.
const agentName = "ctxloom"

// agentVersion is what initialize reports as agentInfo.version: the running
// binary's build stamp, so an editor's "which build am I talking to" question
// gets an answer that actually identifies one. It defaults to the same "dev"
// an unstamped build carries and is set by internal/cli at startup (see
// SetAgentVersion), which is where the ldflags-injected Version lives; this
// package cannot import that one without a cycle. Same shape as
// internal/lm/isolation.SetBinaryVersion, which exists for the same reason.
var agentVersion = "dev"

// SetAgentVersion supplies the build stamp reported at initialize. Called once
// during CLI startup, before any connection is served.
func SetAgentVersion(v string) {
	if v != "" {
		agentVersion = v
	}
}

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
//
// LLMInfo and CommandInfo used to be aliased here too, referenced
// only by this package's own tests (never production) — deleted; tests now
// spell out operations.LLMInfo/operations.CommandInfo directly.
type (
	EngineChat      = operations.EngineChat
	OpenRequest     = operations.OpenRequest
	SessionLLMs     = operations.SessionLLMs
	SessionModes    = operations.SessionModes
	SessionMode     = operations.SessionMode
	SessionCommands = operations.SessionCommands
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

	// cwd is the working directory this session opened with (OpenRequest.Cwd,
	// itself session/new|load's required, absolute `cwd` param). Set once at
	// openSession and never mutated afterward (nothing in this package
	// changes a live session's cwd), so reads need no lock — same convention
	// as commands/fsUpstream below. Session/list's SessionInfo.Cwd is a
	// REQUIRED field on the wire (see handleSessionList); this is the only
	// place that value is retained, since operations.EngineChat carries no
	// Cwd of its own.
	cwd string

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
	// never a refused session). Set once, before the session is published
	// into s.sessions, and never mutated afterwards, so the teardown paths
	// that read it from other goroutines need no lock and can never observe
	// a session whose listener has not been attached yet. Closed once, at
	// teardown (teardownSession); see internal/acpagent/fsupstream.go.
	fsUpstream *fsUpstreamListener
}

// Serve runs the ACP agent over one reader/writer pair (stdio) until the
// client disconnects or ctx is cancelled. Every session's engine conversation
// is torn down on exit.
func Serve(ctx context.Context, r io.Reader, w io.Writer, open ChatOpener) error {
	s := &Server{open: open, ctx: ctx, sessions: make(map[api.SessionId]*session)}
	s.conn = jsonrpc.NewConn(r, w, nil, s)
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
	case api.AgentMethodSessionList:
		s.handleSessionList(params, reply)
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
//   - sessionCapabilities.list: true — session/list is implemented
//     (handleSessionList), backed by the live s.sessions registry every
//     other handler already maintains; SessionInfo.cwd is populated from the
//     session struct's own cwd field (set once at openSession from
//     OpenRequest.Cwd), never left empty to satisfy the wire type. No
//     cursor-based pagination: handleSessionList always answers in one page
//     and never emits nextCursor, which is schema-valid ("If absent, there
//     are no more results") rather than a fabricated cursor over a registry
//     that is never large enough to need one.
//   - sessionCapabilities: close, /delete, /fork, /resume, and
//     /additionalDirectories otherwise stay at their zero value — not
//     implemented yet (handleSessionDelete already answers a probe of
//     /delete honestly).
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
			SessionCapabilities: api.SessionCapabilities{
				List: &api.SessionListCapabilities{},
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

// handleSessionList answers session/list: every currently open (live engine
// conversation) session, honestly. Unlike session/delete, ctxloom's agent
// role DOES have a natural backing store for this one — s.sessions, the same
// registry lookup/closeAllSessions/discardSession already read — so this
// method is implemented rather than refused, and is advertised at initialize
// (sessionCapabilities.list; see handleInitialize's doc comment).
//
// SessionInfo.Cwd is a REQUIRED wire field. Emitting it empty to satisfy the
// type would be exactly this project's silent-no-op failure shape — schema-
// valid, and wrong — so this handler depends on session.cwd having been
// captured at openSession (see that struct field's doc comment) rather than
// improvising a value here.
//
// Only "open right now" sessions are listed: ctxloom's agent role tracks no
// separate at-rest session store this side beyond the live registry (a
// RECORDED harp on disk, reachable via session/load, is a different thing —
// see handleSessionDelete's doc comment on that same distinction), so
// nothing else exists to enumerate.
//
// No pagination: every result rides back in one page and nextCursor is left
// unset, which the spec treats as "no more results" — correct here since
// there never is a second page. An incoming cursor is ignored rather than
// rejected: this agent never emits one, so a well-behaved client's own
// cursor value is always the zero value anyway; a Cwd filter, when supplied,
// IS honored (Filter sessions by working directory), since ignoring a filter
// a client explicitly asked for would itself be the silent-wrong-answer
// shape this handler exists to avoid.
func (s *Server) handleSessionList(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.ListSessionsRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	s.mu.Lock()
	infos := make([]api.SessionInfo, 0, len(s.sessions))
	for id, sess := range s.sessions {
		if req.Cwd != nil && sess.cwd != *req.Cwd {
			continue
		}
		infos = append(infos, api.SessionInfo{SessionId: id, Cwd: sess.cwd})
	}
	s.mu.Unlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].SessionId < infos[j].SessionId })
	reply(api.ListSessionsResponse{Sessions: infos}, nil)
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
		// A cancel is a user action with a visible consequence, so a frame
		// that cannot be decoded must be at least as loud as an unrecognized
		// method above: dropping it silently loses the cancel AND the reason.
		clidiag.Warn("ctxloom", "acp agent: dropping an undecodable session/cancel notification: %v", err)
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
		s.discardSession(sess)
		reply(nil, rerr)
		return
	}
	// B4 (gap G5): available_commands_update has no field on session/new's
	// response (unlike modes/models) — the spec requires it as a
	// session/update notification instead. Sent before the reply so a
	// client's command palette is populated from the earliest possible
	// moment; emitUpdate is a no-op when the session has no commands.
	if rerr := s.emitAvailableCommands(sess); rerr != nil {
		s.discardSession(sess)
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
		s.discardSession(sess)
		reply(nil, rerr)
		return
	}
	// Count what actually reached the wire against what the
	// recorded history HAD, rather than trusting the loop ran. replayEntry
	// (mapping.go) returns nil for entry types/shapes it does not map — a
	// history recorded entirely in unmapped shapes replays zero frames and,
	// without this count, session/load still replies success: the editor
	// sees an empty transcript for a session that provably has one, with no
	// indication anything was lost.
	replayed := 0
	for _, entry := range sess.engine.Replay {
		for _, upd := range sess.replayEntry(entry) {
			if err := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: upd}); err != nil {
				s.discardSession(sess)
				reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "replay: " + err.Error()})
				return
			}
			replayed++
		}
	}
	if len(sess.engine.Replay) > 0 && replayed == 0 {
		clidiag.Warn("ctxloom", "acp agent: session/load %s: %d recorded history entries replayed ZERO frames (all unmapped by replayEntry) — the editor is about to see an empty transcript for a session that is not actually empty", sess.id, len(sess.engine.Replay))
		if rerr := s.emitUpdate(sess, api.SessionUpdate{AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{
			Content: textBlock(fmt.Sprintf("ctxloom: this session has %d recorded history entries that could not be replayed into this transcript (unsupported entry type) — the conversation is not actually empty, only this view of it is.", len(sess.engine.Replay))),
		}}); rerr != nil {
			s.discardSession(sess)
			reply(nil, rerr)
			return
		}
	}
	// B4 (gap G5): see handleSessionNew's identical call for why this rides a
	// notification rather than a loadSessionResult field.
	if rerr := s.emitAvailableCommands(sess); rerr != nil {
		s.discardSession(sess)
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
//
// fsUp (possibly nil) is attached to the session INSIDE the registration
// critical section rather than by the caller afterwards. A session becomes
// reachable to every other goroutine — closeAllSessions, and its own
// child-watch goroutine below — the instant it lands in s.sessions, so any
// field assigned after that point is both an unsynchronized write and a
// window in which teardown sees nothing to tear down.
func (s *Server) openSession(req OpenRequest, fixedID api.SessionId, fsUp *fsUpstreamListener) (*session, *jsonrpc.Error) {
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
	if fixedID != "" && s.sessions[fixedID] != nil {
		// The id was free when we checked above but was claimed while the
		// engine was opening. A FIXED id is the caller's own harp and is the
		// only id session/load's response can be about — that response body
		// carries no sessionId, so the client will go on addressing the harp
		// regardless of what we registered. Minting a generated id here would
		// therefore hand the client a session it can never reach while
		// answering "success"; refusing is the only honest outcome.
		s.mu.Unlock()
		cancel()
		engine.Close()
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "session already active: " + string(fixedID)}
	}
	sess := &session{
		id:           s.resolveSessionID(fixedID, engine.Harp),
		engine:       engine,
		ctx:          ctx,
		cancel:       cancel,
		cancelTurnCh: make(chan struct{}, 1),
		leadContext:  engine.Context,
		modes:        engine.Modes,
		openCall:     make(map[string][]api.ToolCallId),
		commands:     engine.Commands,
		fsUpstream:   fsUp,
		cwd:          req.Cwd,
	}
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	if engine.WatchChildren != nil {
		go s.pushChildUpdates(sess)
	}
	return sess, nil
}

// resolveSessionID picks the id a session registers under. Callers hold s.mu:
// it reads the live registry and advances nextID.
//
// A fixed id (session/load) has already been proven free by openSession and is
// used verbatim. Otherwise the engine's harp is preferred, since that is what
// makes the session addressable by session/load later, and a connection-local
// "ctxloom-N" is minted when there is no harp or the harp is already taken.
// Falling back is safe only here: session/new's response reports the id back,
// so the client always learns which one it got.
func (s *Server) resolveSessionID(fixedID api.SessionId, harp string) api.SessionId {
	if fixedID != "" {
		return fixedID
	}
	if harp != "" && s.sessions[api.SessionId(harp)] == nil {
		return api.SessionId(harp)
	}
	s.nextID++
	return api.SessionId("ctxloom-" + strconv.FormatInt(s.nextID, 10))
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

	parts := promptParts(req.Prompt)
	text := strings.Join(parts, "\n")
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
	// A command invocation is recognized in the prompt's LEADING part only.
	// Handing expandCommand the whole flattened string passed ctxloom's own
	// media placeholder lines to the command as part of its ARGUMENTS, which
	// SessionCommands.Resolve defines as the free text the user typed after
	// the command name. Only that leading part can hold a "/name ..." anyway,
	// so nothing is lost by scoping the match to it — and the placeholders
	// stay exactly where they were in the flattened text the engine receives.
	text, blocks, cerr := s.expandPromptCommand(sess, parts, text, blocks)
	if cerr != nil {
		reply(nil, cerr)
		return
	}

	// A session/prompt whose blocks are ALL unrecognized
	// variants flattens to text == "" via promptText's switch (no default
	// case) and blocks == nil via contentBlocksFromACP's identical switch — a
	// zero-byte message forwarded to the engine as an ordinary, successful
	// turn. Refused here, before a turn is even registered, naming the cause
	// rather than silently running the engine on nothing.
	if text == "" && len(blocks) == 0 {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "session/prompt: the prompt has no content ctxloom recognizes (no text/resource/resourceLink/image/audio block) — refusing rather than sending the engine an empty message"})
		return
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
	// Whether THIS turn carries the lead-context prefix is decided
	// here, but committing sess.contextSent = true must wait until runTurn
	// proves the message actually reached the engine (its own send to
	// sess.engine.In succeeds) — see runTurn's doc. Deciding and committing in
	// the same critical section, as this used to, marked the context
	// delivered before it was, so a cancelled-before-send or dead-engine turn
	// permanently lost the lead block for the rest of the session with no
	// error anywhere.
	prependsContext := !sess.contextSent && sess.leadContext != ""
	if prependsContext {
		// First turn (or first after a mode switch): ctxloom's assembled
		// context rides as the lead block — the oneshot fan-out's proven
		// delivery model, no engine flags needed. The structured form gets
		// the SAME prefix, as its own leading text block, so a ContentBlocks
		// consumer sees the identical lead context a Text-only consumer does.
		text = sess.leadContext + "\n\n" + text
		blocks = append([]agent.ContentBlock{{Kind: "text", Text: sess.leadContext}}, blocks...)
	}
	sess.mu.Unlock()

	go s.runTurn(sess, text, blocks, prependsContext, reply)
}

// expandPromptCommand applies B4's command expansion to a prompt, returning
// the text and blocks the turn should actually carry (both unchanged when no
// command matched).
//
// The match is against the LEADING part only — the sole place a "/name ..."
// invocation can be — so that ctxloom's own media placeholder lines, which
// promptParts interleaves into the same flattened string, never reach the
// command as arguments. On a match the expansion is written back into that
// part and the whole re-joined, which keeps every placeholder exactly where
// it was in the text the engine receives.
func (s *Server) expandPromptCommand(sess *session, parts []string, text string, blocks []agent.ContentBlock) (string, []agent.ContentBlock, *jsonrpc.Error) {
	lead := ""
	if len(parts) > 0 {
		lead = parts[0]
	}
	expandedLead, matched, cerr := expandCommand(sess.ctx, sess.commands, lead)
	if cerr != nil {
		return "", nil, cerr
	}
	if !matched {
		return text, blocks, nil
	}
	parts[0] = expandedLead
	expanded := strings.Join(parts, "\n")
	return expanded, expandedCommandBlocks(blocks, expanded), nil
}

// runTurn runs ONE registered turn: deliver the message, forward the engine's
// events as session/update notifications, forward its permission requests to
// the client, relay a session/cancel to the engine, and reply with the stop
// reason when the turn completes.
func (s *Server) runTurn(sess *session, text string, blocks []agent.ContentBlock, prependsContext bool, replyWire func(any, *jsonrpc.Error)) {
	// The turn must close BEFORE its response reaches the wire: the client may
	// send the next prompt the instant it reads the reply, and that prompt
	// must not race a deferred reset into "a turn is already in flight".
	reply := func(result any, rerr *jsonrpc.Error) {
		sess.mu.Lock()
		sess.inTurn = false
		sess.mu.Unlock()
		replyWire(result, rerr)
	}

	if out := s.deliverTurn(sess, text, blocks, prependsContext); out != nil {
		reply(out.result, out.err)
		return
	}
	for {
		if out := s.awaitTurnStep(sess); out != nil {
			reply(out.result, out.err)
			return
		}
	}
}

// turnOutcome is how a turn ends: exactly one of result/err is meaningful,
// and its presence (a non-nil *turnOutcome) is what says the turn is over.
// The alternative — returning (any, *jsonrpc.Error, bool) from every step —
// makes "not finished yet" and "finished with a nil result" the same value.
type turnOutcome struct {
	result any
	err    *jsonrpc.Error
}

func turnEnds(result any, err *jsonrpc.Error) *turnOutcome {
	return &turnOutcome{result: result, err: err}
}

// deliverTurn hands the turn's message to the engine, returning nil once the
// engine has taken it and a *turnOutcome if the turn ended before that — a
// dead engine (surfaced on Errs), a cancel that beat the send, or shutdown.
func (s *Server) deliverTurn(sess *session, text string, blocks []agent.ContentBlock, prependsContext bool) *turnOutcome {
	select {
	case sess.engine.In <- agent.ChatMessage{Text: text, ContentBlocks: blocks}:
		// ONLY now, having proven the engine actually received this
		// turn's message, is it true that the lead context (if this turn
		// carried it — see handlePrompt) was delivered. Committing this
		// earlier (handlePrompt used to, unconditionally, before this send
		// ever ran) meant a turn that lost the race below — cancelled before
		// send, or a dead engine — still marked the context sent, silently
		// losing it for the rest of the session with no error anywhere.
		if prependsContext {
			sess.mu.Lock()
			sess.contextSent = true
			sess.mu.Unlock()
		}
		return nil
	case <-sess.cancelTurnCh:
		// Cancelled before the engine even received the message: honor the
		// cancel without running the turn.
		return turnEnds(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
	case err := <-sess.engine.Errs:
		return turnEnds(nil, engineError(err))
	case <-s.ctx.Done():
		return turnEnds(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
	}
}

// awaitTurnStep waits for the next thing to happen to a delivered turn and
// advances it by one step: nil means the turn continues, a *turnOutcome that
// it is over.
func (s *Server) awaitTurnStep(sess *session) *turnOutcome {
	select {
	case ev, ok := <-sess.engine.Events:
		if !ok {
			// Conversation ended mid-turn: cancelled/torn down, or died.
			if sess.wasCancelled() {
				return turnEnds(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
			}
			return turnEnds(nil, engineError(sess.finalEngineErr()))
		}
		return s.handleTurnEvent(sess, ev)
	case <-sess.cancelTurnCh:
		s.relayCancelToEngine(sess)
		return nil
	case err := <-sess.engine.Errs:
		if sess.wasCancelled() {
			return turnEnds(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
		}
		return turnEnds(nil, engineError(err))
	case <-s.ctx.Done():
		return turnEnds(api.PromptResponse{StopReason: api.StopReasonCancelled}, nil)
	}
}

// relayCancelToEngine forwards a session/cancel to the engine FROM the turn
// runner, so it is ordered after the turn's own message; the engine then
// completes the turn with a cancelled stop reason. Gives up if the session or
// the server is already going down — there is nothing left to cancel.
func (s *Server) relayCancelToEngine(sess *session) {
	select {
	case sess.engine.In <- agent.ChatMessage{CancelTurn: true}:
	case <-sess.ctx.Done():
	case <-s.ctx.Done():
	}
}

// handleTurnEvent dispatches ONE engine event: nil to keep the turn running,
// a *turnOutcome to end it.
func (s *Server) handleTurnEvent(sess *session, ev agent.ChatEvent) *turnOutcome {
	switch {
	case ev.Session != nil:
		// One-time session metadata (model/mcp): surface it as a
		// session_info_update so a client can render a model header.
		if rerr := s.emitUpdate(sess, sessionInfoUpdateWire(ev.Session)); rerr != nil {
			return turnEnds(nil, rerr)
		}
		return nil
	case ev.Complete != nil:
		// The turn's accounting rides ahead of the completion as a
		// usage_update (context gauge + cost), then the turn ends.
		if rerr := s.emitUpdate(sess, usageUpdateWire(ev.Complete)); rerr != nil {
			return turnEnds(nil, rerr)
		}
		return turnEnds(api.PromptResponse{StopReason: sess.stopReason(ev.Complete.StopReason)}, nil)
	case ev.Permission != nil:
		// Forward to the editor OFF this loop: session/updates must keep
		// streaming while the human decides.
		go s.forwardPermission(sess, ev.Permission)
		return nil
	case ev.Terminal != nil:
		// B1 (gap G6): forward to the editor OFF this loop, exactly
		// like a permission request — session/updates must keep
		// streaming while the editor answers a terminal/* call.
		go s.forwardTerminal(sess, ev.Terminal)
		return nil
	}
	for _, upd := range sess.mapEvent(ev) {
		if rerr := s.emitUpdate(sess, upd); rerr != nil {
			return turnEnds(nil, rerr)
		}
	}
	return nil
}

// forwardPermission relays one engine permission request to the editor as
// session/request_permission and feeds the decision back into the engine. A
// request the connected client actually answers (selected or explicitly
// cancelled) resolves exactly as that answer says.
//
// A request the client could NOT answer (a transport failure, the client
// replying with a protocol error instead of an outcome, or session/server
// teardown racing the call) still resolves as dismissed (empty OptionID) —
// see recordUnansweredPermission's doc for why this does not hang the turn —
// but it is never silently dropped first: RULED 2026-08-30, "queue and
// record; fail loud if unanswerable" — a decision nobody could make must
// leave a durable trace, not just an ephemeral clidiag.Warn line nobody was
// watching (this package's characteristic failure: the agent proceeding
// past a decision nobody made).
func (s *Server) forwardPermission(sess *session, p *agent.PermissionRequest) {
	answer := agent.PermissionAnswer{ID: p.ID}

	var resp struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionId string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := s.conn.Call(sess.ctx, api.ClientMethodSessionRequestPermission, sess.permissionRequestWire(p), &resp); err != nil {
		s.recordUnansweredPermission(sess, p, err)
	} else if resp.Outcome.Outcome == "selected" {
		answer.OptionID = resp.Outcome.OptionId
	}

	select {
	case sess.engine.In <- agent.ChatMessage{Permission: &answer}:
	case <-sess.ctx.Done():
	case <-s.ctx.Done():
	}
}

// recordUnansweredPermission durably records a permission request the
// connected client failed to answer, and surfaces it in the session
// transcript when the connection can still carry a notification — the SAME
// clidiag.Warn + emitUpdate(AgentMessageChunk) pairing handleSessionLoad
// already uses for its own "could not faithfully deliver this to the
// client" gap (see its replay-gap notice above), reused here rather than
// inventing a new channel. zap.L() is the process-wide structured sink
// (internal/shared/logsink) every ctxloom process already writes to
// ~/.ctxloom/logs/ctxloom.log — the durable half of the record, readable
// after the fact by someone who was not watching stderr, which an
// unattended run has nobody doing.
//
// forwardPermission still resolves the request as dismissed (empty
// OptionID) immediately after this call, refusing the requested action
// rather than approving it — and rather than hanging the turn waiting on an
// answer that may never come: see TestServe_PermissionForwarding_ClientError,
// which pins "unparks with a cancelled outcome rather than hanging" as
// existing, load-bearing behavior this fix does not change. A genuine retry-
// until-a-client-reattaches queue would need acpagent.Serve to outlive one
// connection, which is a separate, not-yet-ruled decision — see this
// package's own doc comment and Serve's.
func (s *Server) recordUnansweredPermission(sess *session, p *agent.PermissionRequest, cause error) {
	clidiag.Warn("ctxloom", "acp agent: permission request %s for tool %q could not be answered (session %s): %v — refusing", p.ID, p.ToolName, sess.id, cause)
	zap.L().Error("acp agent: permission request unanswerable, refusing",
		zap.String("session", string(sess.id)),
		zap.String("request_id", p.ID),
		zap.String("tool", p.ToolName),
		zap.Error(cause),
	)
	_ = s.emitUpdate(sess, api.SessionUpdate{AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{
		Content: textBlock(fmt.Sprintf("ctxloom: a permission request for %q could not be delivered to this client (%v) — refusing the action rather than proceeding on a decision nobody made.", p.ToolName, cause)),
	}})
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

	// A lost notification fails the request, exactly as emitUpdate already
	// does for every session/update on this same connection. Answering
	// success would tell the client the switch is done while withholding the
	// only frames that say WHICH profile it is now in — and jsonrpc.Conn
	// writes each frame independently, with no sticky error, so a single
	// frame really can be lost while the reply that follows lands. The switch
	// has already been applied to sess by this point, so the error names that
	// too rather than implying nothing happened.
	if nerr := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: currentModeUpdateWire(modeID)}); nerr != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "switched to " + modeID + " but the mode update could not be delivered: " + nerr.Error()}
	}
	opts := configOptionsWire(&updatedModes, sess.engine.LLMs)
	if nerr := s.conn.Notify(api.ClientMethodSessionUpdate, sessionUpdateParams{SessionId: sess.id, Update: configOptionUpdateWire(opts)}); nerr != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "switched to " + modeID + " but the config option update could not be delivered: " + nerr.Error()}
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
		s.teardownSession(sess)
	}
}

// discardSession unregisters a session that was published into s.sessions but
// whose open never completed — a failure between registration and the
// session/new|load reply means the client is being told the session does not
// exist, so it must not stay registered. Leaving it behind strands a live
// engine conversation until server exit AND, for session/load (whose id is
// the caller's own harp), permanently occupies that harp: openSession refuses
// a fixed id that is already live, so every retry answers "session already
// active" for a session no client was ever handed.
func (s *Server) discardSession(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.id] == sess {
		delete(s.sessions, sess.id)
	}
	s.mu.Unlock()
	s.teardownSession(sess)
}

// teardownSession closes one session's engine conversation and its fs
// reach-back listener. The caller has already removed it from s.sessions.
func (s *Server) teardownSession(sess *session) {
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	sess.cancel()
	sess.engine.Close()
	// B5 (gap G14): tear down this session's fs reach-back listener, if one
	// was ever stood up — nil-safe (Close checks for a nil receiver).
	if err := sess.fsUpstream.Close(); err != nil {
		clidiag.Warn("ctxloom", "acp agent: fs-upstream listener cleanup: %v", err)
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

// engineErrGrace bounds how long a turn waits for the engine's fatal error
// after its Events channel has already closed. See finalEngineErr.
const engineErrGrace = 2 * time.Second

// finalEngineErr collects the engine's conversation-fatal error once Events
// has closed, and returns nil if none is forthcoming.
//
// EngineChat documents Events as "closed when the conversation ends" and says
// nothing about Errs, so a producer may legitimately close one without the
// other: internal/lm/grpc's pumpChatEvents closes both from a single defer,
// but cmd/acpl1harness's engine closes Events and never touches Errs. A bare
// receive here parks the turn forever against the second shape, so the
// session/prompt request never resolves at all and its runner leaks — a
// strictly worse outcome than reporting the conversation ended without a
// specific cause, which is what engineError already says for a nil error.
func (sess *session) finalEngineErr() error {
	select {
	case err := <-sess.engine.Errs:
		return err
	case <-time.After(engineErrGrace):
		return nil
	}
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

// sessionUpdateParams is the session/update notification body. Update is any
// wire-shaped update value (an api.SessionUpdate value, or ctxloom's own
// session-info extension — see ctxloomSessionInfoUpdate).
type sessionUpdateParams struct {
	SessionId api.SessionId `json:"sessionId"`
	Update    any           `json:"update"`
}
