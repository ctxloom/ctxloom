package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file drives one ACP conversation and implements the StructuredChat
// capability (see internal/shared/agent/chat.go). It is the ACP analog of the
// claude stream-json driver: initialize → session/new → per-message
// session/prompt, consuming the agent's session/update stream (mapped in
// mapping.go) until each turn ends, and answering the agent→client callbacks
// (session/request_permission, fs/read_text_file, fs/write_text_file).

// Compile-time assertion that ACP satisfies the optional StructuredChat capability.
var _ agent.StructuredChat = (*ACP)(nil)

// Chat runs one structured ACP conversation for the lifetime of the call. It
// honors the agent.StructuredChat contract: the caller closes `in` to end input;
// this closes `out` exactly once before returning; it returns when input is
// closed and the final turn drains, when ctx is cancelled, or on a fatal error.
//
// Turns run OFF the input loop: while a session/prompt is in flight the loop
// keeps consuming `in`, so a permission answer or a turn cancel can reach a
// parked engine mid-turn (text messages arriving mid-turn queue for the next
// turn). This is what makes ForwardPermissions and CancelTurn possible at all —
// a loop that blocks inside the prompt could never read them.
func (b *ACP) Chat(parentCtx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) (rerr error) {
	defer close(out)

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	open := b.openTransport
	if open == nil {
		open = b.spawnTransport
	}
	tr, err := open(ctx, b.chatArgv(req), b.spawnEnv(req), req.WorkDir)
	if err != nil {
		return err
	}
	// Every failure this conversation can report carries the engine's own
	// dying words when it left any. Registered as a defer (not applied at
	// each of Chat's many return sites) so a future return path cannot
	// silently drop the diagnostic — the whole failure mode here is evidence
	// going missing. Defers run LIFO, so this rewrites rerr strictly BEFORE
	// the `defer close(out)` above fires; no consumer sees a half-annotated
	// result.
	defer func() { rerr = withEngineStderr(rerr, tr) }()

	sess := &chatSession{
		ctx:         ctx,
		out:         out,
		autoApprove: req.Permissions.AllowsWithoutPrompt(),
		forward:     req.ForwardPermissions,
		pendingPerm: make(map[string]chan agent.PermissionAnswer),
		forwardTerm: req.ForwardTerminal,
		pendingTerm: make(map[string]chan agent.TerminalResponse),
		clock:       b.clock(),
		// T13 (finding S3): the fs/* boundary is the same WorkDir handed to
		// the transport two statements above, so it can never name a
		// different directory than the engine actually runs in.
		workspaceRoot: req.WorkDir,
	}
	// B5 (gap G14): req.Env[fsUpstreamEnvVar] is set ONLY on the fully
	// unisolated HOST axis (see operations.OpenEngineSession's gate, and
	// operations.OpenRequest.FsUpstreamAddr's doc for the full rule) — when
	// present, handleFsRead/handleFsWrite chain to the connected editor
	// instead of local disk. A dial failure degrades to local disk (warn,
	// continue) rather than failing the whole conversation: this is an
	// upstream-truth OPTIMIZATION, not a hard requirement to run at all.
	if addr := req.Env[fsUpstreamEnvVar]; addr != "" {
		if fc, ferr := dialFsUpstream(ctx, addr); ferr != nil {
			warnf("acp: fs-upstream dial %q failed, serving fs/* from local disk for this conversation: %v", addr, ferr)
		} else {
			sess.fsUpstream = fc
		}
	}
	conn := jsonrpc.NewConn(tr.stdout, tr.stdin, jsonrpc.CloserFunc(tr.close), sess)
	conn.Start(ctx)

	// teardown cancels (unblocking any handler parked on an out-send), closes the
	// transport (unblocking a parked reader), then waits for the read loop to exit
	// — and, critically (U012-F01), for every in-flight forwardPermission/
	// forwardTerminal goroutine to finish too. conn.Done() alone only proves the
	// READ LOOP stopped; a permission/terminal forwarder spawned with a bare `go`
	// runs independently of it and can still be mid-flight (or not yet scheduled)
	// when the read loop exits. Without this Wait(), Chat's `defer close(out)`
	// could run while such a goroutine's own s.send() call is still pending,
	// racing a send against a close of the same channel — a send on an
	// already-closed channel panics unconditionally. cancel() above closes
	// s.ctx, so any forwarder still parked in its select resolves via
	// <-s.ctx.Done() (not the send) almost immediately; this just waits for
	// that resolution before letting close(out) run.
	teardown := func() {
		cancel()
		_ = conn.Close()
		<-conn.Done()
		sess.forwardGoroutines.Wait()
		if sess.fsUpstream != nil {
			_ = sess.fsUpstream.Close()
		}
	}

	sessionID, caps, engineConfigOptions, mcpDropped, err := b.setup(ctx, conn, req)
	if err != nil {
		teardown()
		return err
	}
	sess.caps = caps
	// CO1 client-role passthrough, READ half: capture whatever config options
	// the CONNECTED ENGINE itself advertised (codex-acp's own real
	// SessionConfigOption entries, e.g.) — see chatSession's field doc for the
	// honest state of the remaining half (surfacing these namespaced to a
	// ctxloom-acp editor, which needs a protobuf change this slice
	// deliberately does not make).
	sess.engineConfigOptions = engineConfigOptions

	// notifyCancel tells the agent to abandon the in-flight turn (best-effort,
	// per-turn: the parked session/prompt then resolves with stopReason
	// "cancelled" and the session stays usable).
	notifyCancel := func() {
		_ = conn.Notify(api.AgentMethodSessionCancel, api.CancelNotification{SessionId: sessionID})
	}
	// abort cancels the in-flight turn AND tears the transport down. Used on any
	// ctx cancellation — between turns or mid-prompt.
	abort := func() {
		notifyCancel()
		teardown()
	}

	// mcpDropped (B3, gap G11) names any editor-supplied http/sse MCP server
	// THIS engine's own advertised mcpCapabilities couldn't take (see
	// mcpServersToACP) — folded into the session's one-time info so a client
	// sees "configured but not delivered" rather than nothing at all. nil
	// when every entry was either stdio or a capability this engine
	// accepted.
	// Resumable carries the engine's initialize-time loadSession capability
	// (caps.LoadSession) so a coordinator can decide — LIVE, before the first
	// turn boundary — whether tearing this engine down and resuming it by
	// SessionID (session/load) is actually safe, or whether it must keep the
	// process warm (one-shot-resume plan, Slice 4 / Fork 3). The same bit that
	// gates the session/load call itself above.
	if !sess.send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: string(sessionID), Resumable: caps.LoadSession, MCPServers: mcpDropped}}) {
		abort()
		return ctx.Err()
	}

	type turnResult struct {
		stop string
		err  error
	}
	var (
		turnDone    chan turnResult     // non-nil while a turn is in flight
		turnStarted time.Time           // when the in-flight turn's prompt was written
		queued      []agent.ChatMessage // messages that arrived mid-turn, in order (B2: full message, not just Text — ContentBlocks rides along)
		inChan      = in                // nil'd once the caller closes input
	)
	// startTurn WRITES the session/prompt frame synchronously (so a CancelTurn
	// processed later by this loop is guaranteed to reach the agent after the
	// prompt it cancels), then awaits the turn's response off the loop. B2:
	// the outbound content blocks are built (and capability-gated against the
	// connected engine — see buildPromptBlocks) from the FULL queued message,
	// not just its flattened Text.
	startTurn := func(msg agent.ChatMessage) {
		done := make(chan turnResult, 1)
		turnDone = done
		turnStarted = sess.clock()
		blocks, berr := sess.buildPromptBlocks(msg)
		if berr != nil {
			done <- turnResult{err: berr}
			return
		}
		await, err := b.promptAsync(conn, sessionID, blocks)
		if err != nil {
			done <- turnResult{err: err}
			return
		}
		go func() {
			stop, perr := await(ctx)
			done <- turnResult{stop: stop, err: perr}
		}()
	}

	for {
		// Idle: start the next queued turn, or finish when input is done.
		if turnDone == nil {
			if len(queued) > 0 {
				next := queued[0]
				queued = queued[1:]
				startTurn(next)
			} else if inChan == nil {
				teardown()
				return nil
			}
		}

		select {
		case <-ctx.Done():
			abort()
			return ctx.Err()
		case res := <-turnDone:
			turnDone = nil
			if res.err != nil {
				// A prompt failure while ctx is cancelled is a cancellation, not a
				// backend error — cancel the turn and report ctx.Err().
				if ctx.Err() != nil {
					abort()
					return ctx.Err()
				}
				teardown()
				return res.err
			}
			if !sess.send(agent.ChatEvent{Complete: sess.completeMeta(res.stop, turnStarted, req.Model)}) {
				abort()
				return ctx.Err()
			}
		case msg, ok := <-inChan:
			if !ok {
				// Input closed: no more turns after the queue drains — and no
				// permission answers can arrive anymore, so pending (and future)
				// forwarded requests resolve as cancelled rather than parking a
				// turn forever.
				inChan = nil
				sess.inputClosed()
				continue
			}
			switch {
			case msg.Permission != nil:
				sess.deliverPermission(*msg.Permission)
			case msg.Terminal != nil:
				sess.deliverTerminal(*msg.Terminal)
			case msg.CancelTurn:
				if turnDone != nil {
					notifyCancel()
				}
			default:
				queued = append(queued, msg)
			}
		}
	}
}

// spawnEnv is the adapter subprocess's env overlay: the caller's env, plus —
// when the embedding backend configured ModelEnvVar — the requested model
// under the engine's native env variable (see ACPConfig.ModelEnvVar: the
// `--model` argv is not honored by every adapter, and a session silently
// running on the user's saved interactive default is exactly the failure the
// model gate exists to prevent). The caller's map is copied, never mutated.
func (b *ACP) spawnEnv(req agent.ChatRequest) map[string]string {
	if b.modelEnvVar == "" || req.Model == "" {
		return req.Env
	}
	env := make(map[string]string, len(req.Env)+1)
	maps.Copy(env, req.Env)
	env[b.modelEnvVar] = req.Model
	return env
}

// engineCapabilities is what the engine's initialize response told us it can
// do, plus the negotiated protocol version — read once by setup and stashed
// on chatSession (see Chat) for the rest of the conversation. A multi-engine
// hub CANNOT advertise per-engine capabilities at ITS OWN initialize time
// (see internal/acpagent/server.go's handleInitialize doc comment: the editor
// picks the engine at session/new, long after ctxloom's own handshake with
// the EDITOR completes) — this is the other half of that story: ctxloom AS A
// CLIENT of the chosen engine reads what that specific engine actually
// offers, so callers can degrade per-session instead of guessing.
//
// B2 (multimodal content, gap G3): Prompt.Image/Audio/EmbeddedContext is the
// FIRST real consumer — buildPromptBlocks/deliverBlock gate every outbound
// image/audio/resource block against it, flattening to a visible warning
// instead of a silent drop when the connected engine didn't advertise
// support. Before this slice the struct was captured but never read past
// LoadSession/AuthMethods.
type engineCapabilities struct {
	Prompt      api.PromptCapabilities
	Mcp         api.McpCapabilities
	Session     api.SessionCapabilities
	Auth        api.AgentAuthCapabilities
	AuthMethods []api.AuthMethod
	LoadSession bool
}

// authRequiredCode is the ACP spec's JSON-RPC error code for "authentication
// required" (agent-go-sdk's errors.go: NewAuthRequired) — an agent may answer
// session/new or session/load with this when it needs authenticate called
// first. ctxloom's hand-rolled jsonrpc package (internal/acp/jsonrpc) only
// defines the three STANDARD JSON-RPC codes because it never needed an
// application-defined one before; this is the first.
const authRequiredCode = -32000

// setup runs the initialize + session/new (or session/load, when resuming)
// handshake, returning the session id the rest of the conversation runs
// under, the engine's advertised capabilities, and (CO1 client-role
// passthrough) whatever SessionConfigOption entries the connected engine
// itself advertised — real spec-compliant options from an engine like
// codex-acp, empty for one like claude-code-acp that advertises none. This
// driver reads the latter purely because a native surface exists to read
// (see doc.go's "mimic native surfaces" principle) — see Chat's call site
// and chatSession.engineConfigOptions for the honest state of surfacing them
// onward.
//
// Protocol version: the client FAILS LOUD on anything but the exact version
// this SDK speaks (api.ProtocolVersionNumber) — never silently continues a
// conversation shaped by a version this decoder does not understand. This is
// the isolation-must-not-negotiate discipline applied to protocol versions:
// an undetected mismatch here would silently mis-decode every frame after it.
func (b *ACP) setup(ctx context.Context, conn *jsonrpc.Conn, req agent.ChatRequest) (api.SessionId, engineCapabilities, []api.SessionConfigOption, []agent.MCPStatus, error) {
	initParams := api.InitializeRequest{
		ProtocolVersion: api.ProtocolVersionNumber,
		ClientCapabilities: api.ClientCapabilities{
			// The client owns the cwd, so it is the natural authority for file
			// reads/writes the agent delegates. auth.terminal is declined:
			// ctxloom drives no interactive authenticate flow yet (see the
			// auth_required handling below), so it must not invite an engine to
			// offer a terminal-shaped auth method it could never answer.
			//
			// Terminal (B1, gap G6): the bidirectional carrier symmetric to
			// Permission's (ChatEvent.Terminal up / ChatMessage.Terminal down,
			// both proto-mirrored — internal/shared/agent/chat.go's
			// TerminalRequest/TerminalResponse, internal/lm/grpc/llm.proto's
			// ChatTerminalRequest/ChatTerminalAnswer) now EXISTS end to end
			// through the plugin gRPC boundary to whichever acpagent.Server
			// process holds the real editor connection (see HandleRequest's
			// terminal/* case below for the brokering itself). But THIS
			// process cannot know, from here, whether that upstream editor
			// actually advertised clientCapabilities.terminal at ITS OWN
			// initialize — that is decided by acpagent.Server, long before
			// this engine conversation was ever opened, and rides down as
			// req.ForwardTerminal (internal/operations.OpenRequest.
			// ForwardTerminal → agent.ChatRequest.ForwardTerminal). So this
			// advertises Terminal: true ONLY when the caller says brokering is
			// actually possible — never unconditionally true, which would
			// promise brokering to an engine whose upstream editor never
			// agreed to answer terminal/* at all (the same advertise-then-drop
			// lie B2 guards against for promptCapabilities.image/audio). A
			// caller with no upstream editor (e.g. a delegated child agent,
			// agentcoord's HarnessSpec) leaves ForwardTerminal false, so this
			// stays false there too — honest, not a regression.
			Terminal: req.ForwardTerminal,
			Fs:       api.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
		ClientInfo: &api.Implementation{Name: clientName, Version: clientVersion},
	}
	var initResp api.InitializeResponse
	if err := conn.Call(ctx, api.AgentMethodInitialize, initParams, &initResp); err != nil {
		return "", engineCapabilities{}, nil, nil, err
	}
	if initResp.ProtocolVersion != api.ProtocolVersionNumber {
		return "", engineCapabilities{}, nil, nil, fmt.Errorf(
			"acp: engine negotiated protocol version %d; ctxloom's ACP client only speaks version %d (schema-v1.19.0) and cannot safely continue a conversation shaped by a version it does not decode",
			initResp.ProtocolVersion, api.ProtocolVersionNumber,
		)
	}
	caps := engineCapabilities{
		Prompt:      initResp.AgentCapabilities.PromptCapabilities,
		Mcp:         initResp.AgentCapabilities.McpCapabilities,
		Session:     initResp.AgentCapabilities.SessionCapabilities,
		Auth:        initResp.AgentCapabilities.Auth,
		AuthMethods: initResp.AuthMethods,
		LoadSession: initResp.AgentCapabilities.LoadSession,
	}

	cwd := req.WorkDir
	if cwd == "" {
		cwd = "." // spec asks for an absolute cwd; the host supplies one in practice.
	}
	mcpServers, mcpDropped := mcpServersToACP(req.MCPServers, caps.Mcp)

	if req.ResumeSessionID != "" {
		// session/load is capability-gated (unlike session/new): an agent
		// that never advertised it would otherwise silently start a FRESH
		// session under the resumed id's name, discarding continuity the
		// caller explicitly asked for — fail loud instead (adapter/SDK gap,
		// reportable, never worked around by falling back to session/new).
		if !caps.LoadSession {
			return "", caps, nil, nil, fmt.Errorf("acp: agent does not advertise the loadSession capability; cannot resume session %q (session/new would silently start a fresh session under a resumed id's name)", req.ResumeSessionID)
		}
		loadReq := api.LoadSessionRequest{Cwd: cwd, McpServers: mcpServers, SessionId: api.SessionId(req.ResumeSessionID)}
		// session/load replays the resumed conversation's history as
		// session/update notifications WHILE this call is in flight (per
		// the ACP spec) — sess is already wired as conn's notification
		// handler, so the replay flows to `out` as ordinary ChatEvents
		// before this call returns; the caller's drain loop must already be
		// running (Chat starts it before setup blocks here).
		var loadResp api.LoadSessionResponse
		if err := conn.Call(ctx, api.AgentMethodSessionLoad, loadReq, &loadResp); err != nil {
			return "", caps, nil, nil, fmt.Errorf("acp: session/load %q: %w", req.ResumeSessionID, authRequiredErr(err, caps))
		}
		sessionID := api.SessionId(req.ResumeSessionID)
		if err := b.applyModelQuirk(ctx, conn, sessionID, initResp.AgentInfo, req); err != nil {
			return "", caps, nil, nil, err
		}
		return sessionID, caps, loadResp.ConfigOptions, mcpDropped, nil
	}

	newReq := api.NewSessionRequest{Cwd: cwd, McpServers: mcpServers}
	var newResp api.NewSessionResponse
	if err := conn.Call(ctx, api.AgentMethodSessionNew, newReq, &newResp); err != nil {
		return "", caps, nil, nil, authRequiredErr(err, caps)
	}
	if err := b.applyModelQuirk(ctx, conn, newResp.SessionId, initResp.AgentInfo, req); err != nil {
		return "", caps, nil, nil, err
	}
	return newResp.SessionId, caps, newResp.ConfigOptions, mcpDropped, nil
}

// authRequiredErr recognizes the spec's auth_required error (code -32000) on
// a session/new or session/load failure and rewrites it into a LOUD,
// actionable error naming the auth method(s) the engine advertised at
// initialize — never a hang, and never a bare opaque RPC error. ctxloom
// drives no interactive authenticate flow yet (that is future work: an
// editor-mediated credential prompt has no seam to ride today), so the only
// honest thing to do with an auth-required engine is fail the session open
// and say exactly what is missing. Any other error passes through unchanged.
func authRequiredErr(err error, caps engineCapabilities) error {
	var rerr *jsonrpc.Error
	if !errors.As(err, &rerr) || rerr.Code != authRequiredCode {
		return err
	}
	return fmt.Errorf("acp: engine requires authentication before a session can be opened (advertised method(s): %s) — ctxloom does not yet drive an interactive authenticate flow; authenticate this engine out of band, then retry: %w", authMethodNames(caps.AuthMethods), err)
}

// authMethodNames renders an engine's advertised auth methods as
// "id (name)" for a human-readable error; AuthMethod is a discriminated
// union (env_var/terminal/agent), each variant carrying its own Id/Name.
func authMethodNames(methods []api.AuthMethod) string {
	if len(methods) == 0 {
		return "none advertised"
	}
	names := make([]string, 0, len(methods))
	for _, m := range methods {
		switch {
		case m.EnvVar != nil:
			names = append(names, string(m.EnvVar.Id)+" ("+m.EnvVar.Name+")")
		case m.Terminal != nil:
			names = append(names, string(m.Terminal.Id)+" ("+m.Terminal.Name+")")
		case m.Agent != nil:
			names = append(names, string(m.Agent.Id)+" ("+m.Agent.Name+")")
		}
	}
	if len(names) == 0 {
		return "unrecognized method shape"
	}
	return strings.Join(names, ", ")
}

// applyModelQuirk fires req.ModelQuirk's non-spec model-selection call — see
// agent.ModelDeliveryQuirk's doc comment for why this exists at all — right
// after session setup and before the first prompt, so the requested model is
// correct from the session's very first turn. This driver holds NO
// engine-specific knowledge of its own: it fires ONLY when the connected
// agent's self-reported identity (this call's initialize response) matches
// AgentName/AdapterVersions exactly; every other combination (including a
// nil ModelQuirk — every backend but claude today) is a silent no-op, and
// the spec-standard delivery (--model argv, ModelConfigKey, ModelEnvVar) is
// left to do the job alone.
func (b *ACP) applyModelQuirk(ctx context.Context, conn *jsonrpc.Conn, sessionID api.SessionId, info *api.Implementation, req agent.ChatRequest) error {
	q := req.ModelQuirk
	if q == nil || q.Method == "" || req.Model == "" {
		return nil
	}
	if info == nil || info.Name != q.AgentName || !slices.Contains(q.AdapterVersions, info.Version) {
		return nil
	}
	params := quirkSetModelParams{SessionId: sessionID, ModelId: req.Model}
	if err := conn.Call(ctx, q.Method, params, nil); err != nil {
		return fmt.Errorf("acp: model-delivery quirk %s: %w", q.Method, err)
	}
	return nil
}

// quirkSetModelParams is the {sessionId, modelId} request shape claude-code-acp's
// UNSTABLE "session/set_model" method expects (its own @agentclientprotocol/sdk's
// schema/zod.gen.js zSetSessionModelRequest; see claudeModelSelectionQuirk's
// doc comment in internal/claude/chat.go for the full method-name story) —
// not a type the pinned coder/acp-go-sdk fork has, hence hand-rolled here.
type quirkSetModelParams struct {
	SessionId api.SessionId `json:"sessionId"`
	ModelId   string        `json:"modelId"`
}

// mcpServersToACP maps the caller-supplied MCP servers onto the session/new
// mcpServers wire shape, gated on the CONNECTED ENGINE's own advertised
// mcpCapabilities (caps, read from this engine's initialize response —
// see setup). Stdio is the protocol's unconditional baseline (every ACP
// agent MUST support it — McpCapabilities has no "stdio" flag), so a stdio
// entry is always forwarded unconditionally, exactly as before B3.
//
// Http/Sse (B3, gap G11) are forwarded ONLY when this specific engine
// advertised the matching capability at initialize — the spec is explicit
// that these variants are "only available when the Agent capabilities
// indicate" them, so sending one to an engine that never claimed it would
// be a protocol violation, not a graceful degrade. An entry this engine
// can't take is NEVER silently dropped: it comes back in `dropped` as an
// agent.MCPStatus the caller (Chat) folds into the session's
// ChatSessionInfo, so the user sees "you configured tool X but this engine
// doesn't support it" instead of nothing.
//
// Slices stay non-nil (the spec wants arrays, and a nil slice marshals as
// JSON null); env/headers are sorted for a deterministic frame.
func mcpServersToACP(servers []agent.ChatMCPServer, caps api.McpCapabilities) (out []api.McpServer, dropped []agent.MCPStatus) {
	out = make([]api.McpServer, 0, len(servers))
	for _, s := range servers {
		switch s.Transport {
		case agent.MCPTransportHTTP:
			if !caps.Http {
				dropped = append(dropped, agent.MCPStatus{Name: s.Name,
					Status: "unsupported: connected engine does not advertise mcpCapabilities.http — configured HTTP MCP server was NOT sent (would be a protocol violation, not a graceful degrade)"})
				continue
			}
			out = append(out, api.McpServer{Http: &api.McpServerHttpInline{Name: s.Name, Url: s.URL, Headers: headersToACP(s.Headers)}})
		case agent.MCPTransportSSE:
			if !caps.Sse {
				dropped = append(dropped, agent.MCPStatus{Name: s.Name,
					Status: "unsupported: connected engine does not advertise mcpCapabilities.sse — configured SSE MCP server was NOT sent (would be a protocol violation, not a graceful degrade)"})
				continue
			}
			out = append(out, api.McpServer{Sse: &api.McpServerSseInline{Name: s.Name, Url: s.URL, Headers: headersToACP(s.Headers)}})
		default:
			args := s.Args
			if args == nil {
				args = []string{}
			}
			env := make([]api.EnvVariable, 0, len(s.Env))
			for _, k := range slices.Sorted(maps.Keys(s.Env)) {
				env = append(env, api.EnvVariable{Name: k, Value: s.Env[k]})
			}
			out = append(out, api.McpServer{Stdio: &api.McpServerStdio{Name: s.Name, Command: s.Command, Args: args, Env: env}})
		}
	}
	return out, dropped
}

// headersToACP converts agent.ChatMCPServer.Headers to the ACP wire's sorted
// HTTP header list (non-nil, mirroring mcpServersToACP's env handling).
func headersToACP(headers map[string]string) []api.HttpHeader {
	out := make([]api.HttpHeader, 0, len(headers))
	for _, k := range slices.Sorted(maps.Keys(headers)) {
		out = append(out, api.HttpHeader{Name: k, Value: headers[k]})
	}
	return out
}

// promptAsync sends one user message as a session/prompt turn: the request
// frame is written before it returns, and the returned await blocks until the
// turn ends, yielding the stop reason. The turn's session/update stream is
// consumed concurrently by the read loop (chatSession.HandleNotification),
// which forwards mapped entries to `out` before the response arrives.
// blocks is the already-built (and, per B2, already capability-gated — see
// chatSession.buildPromptBlocks) outbound content-block array; the caller
// decides its shape, this function only ships it.
func (b *ACP) promptAsync(conn *jsonrpc.Conn, sessionID api.SessionId, blocks []api.ContentBlock) (func(context.Context) (string, error), error) {
	promptReq := api.PromptRequest{
		SessionId: sessionID,
		Prompt:    blocks,
	}
	await, err := conn.Go(api.AgentMethodSessionPrompt, promptReq)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		var resp api.PromptResponse
		if aerr := await(ctx, &resp); aerr != nil {
			return "", aerr
		}
		return string(resp.StopReason), nil
	}, nil
}

// --- B2: multimodal prompt delivery (CLIENT role) ---

// buildPromptBlocks renders one outbound ChatMessage as the ACP content-block
// array actually delivered to the connected engine. msg.ContentBlocks (IR2's
// structured carrier, landed for exactly this) is used when the caller
// populated it — today, only acpagent's session/prompt intake does (see
// internal/acpagent/server.go's handlePrompt); every OTHER caller (`run
// --structured`, oneshot Execute, the interactive pty driver — none of which
// ever set ContentBlocks) falls back to the single flattened text block sent
// exactly as before this slice, so their behavior is byte-for-byte unchanged
// (the hard constraint every ACP Hub slice restates).
//
// Per-block degradation is CAPABILITY-GATED against the connected engine's
// OWN advertised prompt capabilities (s.caps.Prompt, captured once in setup —
// see engineCapabilities' doc comment, landed by the C-slice specifically for
// this consumption): image/audio/embedded-resource ride through untouched
// when the engine actually advertised support; otherwise the block is
// FLATTENED to a visible placeholder text block naming what was dropped and
// why (kind, mime type, byte count) — never a silent drop (this codebase's
// signature bug: exit 0, success, zero bytes delivered). text and
// resource_link are unconditional per spec (every agent MUST support them).
func (s *chatSession) buildPromptBlocks(msg agent.ChatMessage) ([]api.ContentBlock, error) {
	if len(msg.ContentBlocks) == 0 {
		// A prompt with no blocks and no text is zero bytes of intent. Sending
		// it as TextBlock("") burns a real engine turn and returns a normal
		// completion — the house silent-no-op. Refuse it instead; callers that
		// legitimately have nothing to say must not call at all (see
		// cli.readMessagesLoop, which drops blank REPL lines before they get
		// here).
		if strings.TrimSpace(msg.Text) == "" {
			return nil, fmt.Errorf("acp: refusing to deliver an empty prompt (no text and no content blocks) — a turn would run on zero bytes")
		}
		return []api.ContentBlock{api.TextBlock(msg.Text)}, nil
	}
	out := make([]api.ContentBlock, 0, len(msg.ContentBlocks))
	for _, b := range msg.ContentBlocks {
		out = append(out, s.deliverBlock(b))
	}
	return out, nil
}

// deliverBlock renders one IR content block for delivery: image/audio/
// resource decode their full-fidelity Raw bytes (see agent.ContentBlock's doc
// comment — Raw is the ORIGINAL ACP block, verbatim, produced by
// internal/acp/mapping.go's blockToIR on intake) back into a real typed ACP
// block when the connected engine's capabilities allow it; otherwise it
// degrades to a labeled text placeholder rather than vanishing. text and
// resource_link are spec-mandatory for every agent, so they are never gated.
func (s *chatSession) deliverBlock(b agent.ContentBlock) api.ContentBlock {
	switch b.Kind {
	case "image":
		if s.caps.Prompt.Image {
			if block, ok := decodeACPBlock(b.Raw); ok {
				return block
			}
		}
		return flattenedBlockWarning(b, "image")
	case "audio":
		if s.caps.Prompt.Audio {
			if block, ok := decodeACPBlock(b.Raw); ok {
				return block
			}
		}
		return flattenedBlockWarning(b, "audio")
	case "resource":
		if s.caps.Prompt.EmbeddedContext {
			if block, ok := decodeACPBlock(b.Raw); ok {
				return block
			}
		}
		return flattenedBlockWarning(b, "resource")
	case "resource_link":
		// Spec: "All agents MUST support resource links" — never gated.
		if block, ok := decodeACPBlock(b.Raw); ok {
			return block
		}
		return api.TextBlock(b.Text)
	case "text", "":
		return api.TextBlock(b.Text)
	default:
		// A kind this build does not know about. Flattening it to
		// TextBlock(b.Text) delivers an EMPTY text block for any structured
		// block (whose payload lives in Raw, not Text) — a silent drop, the
		// exact thing this function's doc promises never to do. Deliver a
		// visible placeholder naming the kind instead, carrying whatever text
		// the block did have.
		warnf("acp: unrecognized content block kind %q flattened to a text placeholder — this build cannot deliver it", b.Kind)
		if strings.TrimSpace(b.Text) != "" {
			return api.TextBlock(fmt.Sprintf("[unrecognized %q content block, delivered as text]\n%s", b.Kind, b.Text))
		}
		return api.TextBlock(fmt.Sprintf("[%q content received but not delivered: this build does not recognize that block kind%s]", b.Kind, mediaBlockDetail(b.Raw)))
	}
}

// decodeACPBlock re-decodes an IR content block's full-fidelity Raw bytes
// back into a real typed api.ContentBlock (the union's UnmarshalJSON
// dispatches on the embedded "type" discriminator, so this round-trips
// exactly what internal/acp/mapping.go's blockToIR marshaled on intake).
// false on empty/malformed Raw — the caller degrades to a text placeholder
// rather than sending a block this decode couldn't stand behind.
func decodeACPBlock(raw json.RawMessage) (api.ContentBlock, bool) {
	if len(raw) == 0 {
		return api.ContentBlock{}, false
	}
	var block api.ContentBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return api.ContentBlock{}, false
	}
	return block, true
}

// flattenedBlockWarning renders b as a visible text placeholder when the
// connected engine did not advertise support for its kind (or its Raw bytes
// failed to decode) — the flatten-WITH-warning degradation the plan requires:
// ctxloom neither silently drops the content nor lies about having delivered
// it. Logs the same finding (operational visibility) and returns text so the
// model — and a transcript viewer — sees that something arrived instead of
// the turn silently missing it.
func flattenedBlockWarning(b agent.ContentBlock, kind string) api.ContentBlock {
	detail := mediaBlockDetail(b.Raw)
	warnf("acp: flattening %s content block to text — the connected engine does not advertise %s support%s", kind, kind, detail)
	return api.TextBlock(fmt.Sprintf("[%s content received but not delivered: the connected engine does not advertise %s support%s]", kind, kind, detail))
}

// mediaBlockDetail extracts a human-readable "(mimeType=..., N bytes)" detail
// from an image/audio block's Raw bytes for the flatten-with-warning message,
// or "" when Raw carries no recognizable mimeType (e.g. a resource block,
// which has no data/mimeType shape).
func mediaBlockDetail(raw json.RawMessage) string {
	var d struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.MimeType == "" {
		return ""
	}
	return fmt.Sprintf(" (mimeType=%s, %d bytes)", d.MimeType, len(d.Data))
}

// --- per-conversation callback handler ---

// chatSession is the live conversation state and the rpcHandler for one Chat
// call: it forwards the agent's session/update stream to `out` and answers the
// agent's request callbacks. One per Chat, so `out`/ctx can be captured directly.
type chatSession struct {
	ctx         context.Context
	out         chan<- agent.ChatEvent
	autoApprove bool
	forward     bool // surface permission requests upstream instead of auto-deciding
	clock       func() time.Time

	// caps is the engine's advertised initialize-time capabilities, read once
	// in Chat right after setup() returns and never mutated afterward — safe
	// to read from any goroutine without a lock (see setup/engineCapabilities).
	caps engineCapabilities

	// forwardGoroutines counts every in-flight forwardPermission/forwardTerminal
	// goroutine (U012-F01). Chat's `defer close(out)` used to race them: each is
	// spawned with a bare `go` and never joined, so a goroutine's own s.send()
	// call — its first statement, before it ever parks on ctx.Done() — could
	// still be running (or not yet scheduled) after Chat's teardown() cancelled
	// ctx and Chat itself returned, closing out from underneath it. A send on an
	// already-closed channel panics unconditionally; the review measured 96/200
	// panics reproducing the exact select shape. teardown() now Wait()s on this
	// after the read loop exits, so close(out) cannot precede the LAST sender.
	forwardGoroutines sync.WaitGroup

	// forwarded-permission state: each in-flight request parks on its channel
	// until the caller's answer (or input close / ctx death) resolves it.
	permMu      sync.Mutex
	permSeq     int64
	pendingPerm map[string]chan agent.PermissionAnswer
	noInput     bool // input closed: no answers can arrive anymore

	// forwardTerm mirrors forward, gating the terminal/* broker instead of
	// permission forwarding (B1, gap G6) — set from
	// agent.ChatRequest.ForwardTerminal, true only when the upstream editor
	// actually advertised the terminal capability (see setup's doc comment).
	// forwarded-terminal state mirrors the permission bookkeeping above
	// exactly, in its own mutex/map so the two domains never share a lock.
	forwardTerm bool

	// workspaceRoot is this session's authoritative filesystem boundary: the
	// SAME agent.ChatRequest.WorkDir the engine subprocess is spawned with
	// as its cmd.Dir (Chat's `open(ctx, argv, env, req.WorkDir)`), so the
	// boundary and the engine's own working directory cannot drift apart.
	// Every fs/* handler resolves against it via confineToWorkspace
	// (fsconfine.go) — T13, finding S3. Set once in Chat, before conn's read
	// loop starts, and never mutated: no lock needed, same as fsUpstream.
	//
	// Empty means the caller named no workspace, which is precisely when the
	// engine is spawned with an empty cmd.Dir and inherits this process's
	// cwd; confineToWorkspace resolves "" to that same cwd, so the boundary
	// still tracks the engine rather than defaulting to permitting anything.
	workspaceRoot string

	termMu      sync.Mutex
	termSeq     int64
	pendingTerm map[string]chan agent.TerminalResponse
	termNoInput bool // input closed: no terminal answers can arrive anymore

	// turn accounting fed by the real usage_update variant — see
	// consumeMetaUpdate. Guarded by metaMu: updates arrive on the read loop
	// while completeMeta runs on the Chat loop. G13: this used to also carry
	// infoModel/infoWindow extracted from ctxloom's own session-info `_meta`
	// extension; that extraction is gone (see consumeMetaUpdate's doc
	// comment) now that Model/ContextWindow ride CO1's SessionConfigOption
	// and usage_update's `size` respectively instead.
	metaMu sync.Mutex
	usage  *api.UsageUpdate // latest usage report (cumulative; freshest wins)

	// fsUpstream, when non-nil, is this conversation's local fs reach-back
	// link to the connected editor (B5, gap G14) — handleFsRead/
	// handleFsWrite relay through it instead of reading/writing local disk.
	// nil is the overwhelmingly common case: every launch path OTHER than
	// an ACP-hosted, fully-unisolated (host axis) session — interactive pty,
	// `run --structured`, oneshot Execute, and any WORKTREE- or
	// CONTAINER-isolated ACP session — leaves this nil and serves local
	// disk exactly as before this field existed. Set once in Chat, before
	// conn's read loop starts (handleFsRead/Write only ever run on that read
	// loop), so it needs no lock.
	fsUpstream *jsonrpc.Conn

	// engineConfigOptions is CO1's client-role passthrough READ half: the
	// connected engine's own SessionConfigOption entries (session/new or
	// session/load's configOptions — see setup's doc comment), captured
	// generically with no per-engine knowledge. It is NOT yet forwarded
	// anywhere: doing so onto ctxloom's own agent role (as engine/<id>
	// namespaced options — the SET half of D-CO's design) needs a new
	// agent.ChatSessionInfo/ChatEvent field that survives the self-invoking
	// "ctxloom llm serve" runner's protobuf boundary (internal/lm/grpc), a
	// change this slice deliberately does not make (see CO1's final report
	// for why: proto regen risk + internal/operations/engine_session.go,
	// which would need to relay it, is owned by a concurrent slice). This
	// field exists so the read is provably not silently dropped, and so a
	// follow-up slice has a captured value to wire onward instead of adding
	// the read from scratch.
	engineConfigOptions []api.SessionConfigOption
}

// send emits one event, stamping a receipt time when the entry lacks one (ACP
// updates carry no per-event timestamp). Returns false if ctx is cancelled first.
func (s *chatSession) send(ev agent.ChatEvent) bool {
	if ev.Entry != nil && ev.Entry.Timestamp.IsZero() {
		ev.Entry.Timestamp = s.clock()
	}
	select {
	case s.out <- ev:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// HandleNotification maps a session/update onto chat entries and forwards them.
// Unknown notifications and malformed updates are dropped (warn + continue).
func (s *chatSession) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	if method != api.ClientMethodSessionUpdate {
		return
	}
	raw, err := rawSessionUpdate(params)
	if err != nil {
		warnf("acp: dropping malformed session/update: %v", err)
		return
	}
	// The real usage_update variant folds into the turn meta instead of
	// running through mapSessionUpdate as an entry (G13: session_info_update
	// no longer needs a special case here — see consumeMetaUpdate's doc
	// comment).
	if s.consumeMetaUpdate(raw) {
		return
	}
	var upd api.SessionUpdate
	if err := json.Unmarshal(raw, &upd); err != nil {
		warnf("acp: dropping malformed session/update: %v", err)
		return
	}
	for _, ev := range mapSessionUpdate(&upd) {
		if !s.send(ev) {
			return
		}
	}
}

// consumeMetaUpdate absorbs the real spec usage_update variant into the turn
// accounting rather than mapping it to an entry (now decoded straight into
// api.UsageUpdate — H1 confirmed it already matches the spec exactly).
// Returns true when the update was usage_update (there is no entry to emit);
// a malformed usage_update is warned and dropped, never crashing the stream.
//
// G13 (decomposing IR4): this used to ALSO special-case ctxloom's own
// "session_info_update"-discriminated frame, extracting Model/ContextWindow
// from its `_meta.ctxloom_session_info` object into s.infoModel/s.infoWindow.
// That case is REMOVED, not left as a no-op guard: the emitter
// (internal/acpagent/mapping.go's sessionInfoUpdateWire) no longer puts
// Model or ContextWindow in `_meta` at all, so there is nothing left to
// extract — Model rides CO1's SessionConfigOption ("model" category) and
// ContextWindow rides usage_update's own `size` field below, both
// unconditionally. A session_info_update frame — ctxloom's own
// PermissionMode/MCPServers residue, or a genuinely foreign engine's real
// title/timestamp update — now simply falls through to the strict
// api.SessionUpdate decode in HandleNotification and lands in
// mapSessionUpdate's default case (dropped as an unmodeled variant): the
// exact same harmless outcome an unrecognized session_info_update already
// had before G13 (see TestChat_ForeignSessionInfoUpdateIgnored). This does
// NOT propagate PermissionMode/MCPServers into agent.ChatSessionInfo for a
// nested ctxloom-driving-ctxloom hop — that was ALREADY unread before this
// slice ("the frame also carries permissionMode/mcpServers, not yet read
// here"), and stays that way; out of scope for G13's foreign-client-render
// mission (see the G13 report's deferred-work list).
func (s *chatSession) consumeMetaUpdate(raw json.RawMessage) bool {
	if updateDiscriminator(raw) != usageUpdateVariant {
		return false
	}
	var u api.UsageUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		warnf("acp: dropping malformed usage_update: %v", err)
		return true
	}
	s.metaMu.Lock()
	s.usage = &u
	s.metaMu.Unlock()
	return true
}

// completeMeta assembles one turn's completion accounting: the wire-sourced
// stop reason; the requested model (G13: no longer overridable by ctxloom's
// own session-info echo — see consumeMetaUpdate's doc comment; the model a
// foreign client renders as "current" comes from CO1's SessionConfigOption
// instead); the latest usage the agent reported (cumulative session state,
// so the freshest value stands across turns); and the self-measured
// wall-clock duration (the protocol carries no timing — see mapping.go on
// what ACP v1 does and does not deliver).
func (s *chatSession) completeMeta(stop string, started time.Time, requestedModel string) *agent.TurnMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m := &agent.TurnMeta{
		StopReason: stop,
		Model:      requestedModel,
		DurationMs: int(s.clock().Sub(started).Milliseconds()),
	}
	if u := s.usage; u != nil {
		m.InputTokens = u.Used
		if u.Size > 0 {
			m.ContextWindow = u.Size
		}
		if u.Cost != nil && (u.Cost.Currency == "" || u.Cost.Currency == "USD") {
			m.CostUSD = u.Cost.Amount
		}
	}
	return m
}

// HandleRequest answers an agent→client request. fs I/O and auto-decided
// permissions reply inline (quick, local); a FORWARDED permission replies
// asynchronously — it parks on the caller's answer, and the read loop must stay
// free to keep streaming session/updates while the human decides. Unknown
// methods (e.g. the declined terminal/*) get a JSON-RPC method-not-found error
// rather than crashing.
func (s *chatSession) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.ClientMethodSessionRequestPermission:
		s.handlePermission(params, reply)
	case api.ClientMethodFsReadTextFile:
		reply(s.handleFsRead(params))
	case api.ClientMethodFsWriteTextFile:
		reply(s.handleFsWrite(params))
	case api.ClientMethodTerminalCreate, api.ClientMethodTerminalOutput,
		api.ClientMethodTerminalWaitForExit, api.ClientMethodTerminalKill, api.ClientMethodTerminalRelease:
		s.handleTerminal(method, params, reply)
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "acp: method not supported: " + method})
	}
}

// handlePermission answers a permission request. Under ForwardPermissions it
// surfaces the request upstream as a ChatEvent and parks (off the read loop)
// until the caller's PermissionAnswer resolves it. Otherwise it auto-decides —
// allow under a bypass posture, else reject — mirroring the claude driver, since
// a non-interactive chat has no human to prompt.
func (s *chatSession) handlePermission(params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	var req api.RequestPermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	if ch, id := s.registerPermission(); ch != nil {
		go s.forwardPermission(id, ch, &req, reply)
		return
	}
	reply(decidePermission(req.Options, s.autoApprove), nil)
}

// registerPermission allocates a pending forwarded-permission slot, or returns
// nil when forwarding is off (or no answer can ever arrive — input closed).
func (s *chatSession) registerPermission() (chan agent.PermissionAnswer, string) {
	if !s.forward {
		return nil, ""
	}
	s.permMu.Lock()
	defer s.permMu.Unlock()
	if s.noInput {
		return nil, ""
	}
	s.permSeq++
	id := "perm-" + strconv.FormatInt(s.permSeq, 10)
	ch := make(chan agent.PermissionAnswer, 1)
	s.pendingPerm[id] = ch
	// Counted BEFORE the caller spawns forwardPermission (U012-F01): the caller
	// does `if ch, id := s.registerPermission(); ch != nil { go s.forwardPermission(...) }`,
	// so Add must happen here, synchronously, rather than as the first statement
	// of the goroutine — otherwise teardown's Wait() could observe zero
	// in-flight forwarders during the (however brief) window between `go` and
	// the new goroutine actually starting to run.
	s.forwardGoroutines.Add(1)
	return ch, id
}

// forwardPermission emits the request upstream and replies with the caller's
// decision. A closed answer channel (input closed), a dismissed answer (empty
// OptionID), or ctx death all resolve as a "cancelled" outcome — the safe
// no-op that neither approves nor commits a remembered rejection.
func (s *chatSession) forwardPermission(id string, ch chan agent.PermissionAnswer, req *api.RequestPermissionRequest, reply func(any, *jsonrpc.Error)) {
	defer s.forwardGoroutines.Done()
	defer s.unregisterPermission(id)
	cancelled := permissionResult{Outcome: permissionOutcome{Outcome: outcomeCancelled}}
	if !s.send(agent.ChatEvent{Permission: permissionRequestEvent(id, req)}) {
		reply(cancelled, nil)
		return
	}
	select {
	case ans, ok := <-ch:
		if !ok || ans.OptionID == "" {
			reply(cancelled, nil)
			return
		}
		reply(permissionResult{Outcome: permissionOutcome{Outcome: outcomeSelected, OptionId: ans.OptionID}}, nil)
	case <-s.ctx.Done():
		reply(cancelled, nil)
	}
}

// deliverPermission routes the caller's answer to the parked request; an
// answer for an unknown (already-resolved) request is dropped with a warning.
// The send happens under permMu so it cannot race inputClosed's close of the
// same channel; it never blocks (buffered(1), duplicates dropped).
func (s *chatSession) deliverPermission(ans agent.PermissionAnswer) {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	ch := s.pendingPerm[ans.ID]
	if ch == nil {
		warnf("acp: dropping permission answer for unknown request %q", ans.ID)
		return
	}
	select {
	case ch <- ans:
	default: // buffered(1): a duplicate answer is dropped
	}
}

// unregisterPermission retires a resolved forwarded request.
func (s *chatSession) unregisterPermission(id string) {
	s.permMu.Lock()
	delete(s.pendingPerm, id)
	s.permMu.Unlock()
}

// inputClosed marks that no more answers can arrive: every parked forwarded
// request resolves as cancelled, and future ones auto-decide instead.
func (s *chatSession) inputClosed() {
	s.permMu.Lock()
	s.noInput = true
	for id, ch := range s.pendingPerm {
		close(ch)
		delete(s.pendingPerm, id)
	}
	s.permMu.Unlock()

	s.termMu.Lock()
	s.termNoInput = true
	for id, ch := range s.pendingTerm {
		close(ch)
		delete(s.pendingTerm, id)
	}
	s.termMu.Unlock()
}

// handleTerminal answers one terminal/* request (B1, gap G6). Under
// ForwardTerminal (the upstream editor advertised the capability) it
// surfaces the request as a ChatEvent.Terminal and parks (off the read loop)
// until the caller's TerminalResponse resolves it — exactly handlePermission's
// shape, applied to a different upstream callback. Otherwise it declines with
// a SPECIFIC, actionable error naming the real reason — never the generic
// method-not-found a truly-unrecognized method gets, and never a
// locally-implemented fake terminal: ctxloom must broker, never implement one
// of its own.
func (s *chatSession) handleTerminal(method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	if ch, id := s.registerTerminal(); ch != nil {
		go s.forwardTerminal(id, ch, method, params, reply)
		return
	}
	reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "acp: " + method + " not supported: ctxloom's client role does not advertise the terminal capability for this session (no upstream editor advertised clientCapabilities.terminal, or input has already closed) — ctxloom brokers terminal/* to editors, it never implements one itself"})
}

// registerTerminal allocates a pending forwarded-terminal-request slot, or
// returns nil when forwarding is off (or no answer can ever arrive — input
// closed). Mirrors registerPermission exactly.
func (s *chatSession) registerTerminal() (chan agent.TerminalResponse, string) {
	if !s.forwardTerm {
		return nil, ""
	}
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if s.termNoInput {
		return nil, ""
	}
	s.termSeq++
	id := "term-" + strconv.FormatInt(s.termSeq, 10)
	ch := make(chan agent.TerminalResponse, 1)
	s.pendingTerm[id] = ch
	// See registerPermission's identical comment: counted here, synchronously,
	// before the caller's `go s.forwardTerminal(...)` — not as that goroutine's
	// first statement.
	s.forwardGoroutines.Add(1)
	return ch, id
}

// forwardTerminal emits one terminal/* request upstream (stripped of this
// process's own opaque session id — the upstream editor does not share it;
// see agent.TerminalRequest's doc comment) and replies with the editor's
// answer. A closed answer channel (input closed) or ctx death replies with a
// loud, specific error — never a silent drop, and never a hang: an engine
// waiting on terminal/create must get SOME answer.
func (s *chatSession) forwardTerminal(id string, ch chan agent.TerminalResponse, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	defer s.forwardGoroutines.Done()
	defer s.unregisterTerminal(id)
	stripped, err := stripSessionID(params)
	if err != nil {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	req := &agent.TerminalRequest{ID: id, Op: terminalOp(method), Params: stripped}
	if !s.send(agent.ChatEvent{Terminal: req}) {
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "acp: " + method + ": chat ended before the terminal request could reach the editor"})
		return
	}
	select {
	case ans, ok := <-ch:
		if !ok {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "acp: " + method + ": input closed before the editor answered the terminal request"})
			return
		}
		if ans.Error != "" {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "acp: " + method + ": editor terminal request failed: " + ans.Error})
			return
		}
		reply(ans.Result, nil)
	case <-s.ctx.Done():
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "acp: " + method + ": chat context cancelled before the editor answered the terminal request"})
	}
}

// deliverTerminal routes the caller's answer to the parked terminal request;
// an answer for an unknown (already-resolved) request is dropped with a
// warning. Mirrors deliverPermission exactly.
func (s *chatSession) deliverTerminal(ans agent.TerminalResponse) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	ch := s.pendingTerm[ans.ID]
	if ch == nil {
		warnf("acp: dropping terminal answer for unknown request %q", ans.ID)
		return
	}
	select {
	case ch <- ans:
	default: // buffered(1): a duplicate answer is dropped
	}
}

// unregisterTerminal retires a resolved forwarded terminal request.
func (s *chatSession) unregisterTerminal(id string) {
	s.termMu.Lock()
	delete(s.pendingTerm, id)
	s.termMu.Unlock()
}

// terminalOp maps an ACP terminal/* JSON-RPC method name onto the
// agent.TerminalOp* vocabulary the IR/proto carrier uses.
func terminalOp(method string) string {
	switch method {
	case api.ClientMethodTerminalCreate:
		return agent.TerminalOpCreate
	case api.ClientMethodTerminalOutput:
		return agent.TerminalOpOutput
	case api.ClientMethodTerminalWaitForExit:
		return agent.TerminalOpWaitForExit
	case api.ClientMethodTerminalKill:
		return agent.TerminalOpKill
	case api.ClientMethodTerminalRelease:
		return agent.TerminalOpRelease
	default:
		return method
	}
}

// stripSessionID removes the "sessionId" property from a terminal/*
// request's raw params before it crosses ChatEvent.Terminal: sessionId here
// names THIS client-role process's own opaque session with the connected
// ENGINE, which the upstream editor does not share and must never see — the
// agent-role broker (internal/acpagent) substitutes ITS OWN editor-facing
// session id before relaying, exactly as permissionRequestWire substitutes
// sess.id for a forwarded permission request instead of carrying the
// engine's own id through.
func stripSessionID(params json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, fmt.Errorf("decode terminal request params: %w", err)
	}
	delete(m, "sessionId")
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("re-encode terminal request params: %w", err)
	}
	return out, nil
}

// handleFsRead serves fs/read_text_file (B5, gap G14 — THE ONE RULE: fs
// follows the engine's authoritative workspace):
//
//   - HOST axis, fs-upstream dialed (s.fsUpstream != nil): the connected
//     EDITOR is authoritative — chained upstream so an unsaved buffer's live
//     content answers instead of whatever is (or isn't yet) on disk. This is
//     the ONLY branch this slice adds; the other two are the PRE-EXISTING
//     local-disk read, now with the axis reasoning made explicit:
//   - WORKTREE axis: req.Path arrives already rooted at the worktree
//     (the engine's own cwd — see OpenEngineSession's workDir), so
//     os.ReadFile below reads the WORKTREE's real content. Chaining to
//     the editor here would be WRONG — its buffers describe the MAIN
//     checkout, a different tree entirely.
//   - CONTAINER axis: ISO1's same-path mount means req.Path is ALSO valid
//     on this host filesystem (this driver always runs on the host —
//     only the ENGINE's own subprocess is containerized), so local disk
//     is already correct.
//
// CONFINEMENT (T13, finding S3) applies on every one of those axes and is
// decided BEFORE the branch: req.Path is resolved against s.workspaceRoot by
// confineToWorkspace (fsconfine.go) and rewritten to the checked real path,
// so an engine cannot reach outside the session's workspace by any axis —
// including through the editor. The container axis is why this is not
// optional: once ChatStart carries the runtime axis again, the engine is in
// a container while this handler is on the host, and an unconfined read is
// a straight credential exfiltration.
//
// Honors the optional 1-based line offset and line limit either way.
func (s *chatSession) handleFsRead(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.ReadTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	safePath, cerr := confineToWorkspace(s.workspaceRoot, req.Path)
	if cerr != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "fs/read_text_file: " + cerr.Error()}
	}
	req.Path = safePath
	if s.fsUpstream != nil {
		var resp api.ReadTextFileResponse
		if err := s.fsUpstream.Call(s.ctx, api.ClientMethodFsReadTextFile, req, &resp); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		}
		return resp, nil
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return api.ReadTextFileResponse{Content: sliceLines(string(data), req.Line, req.Limit)}, nil
}

// handleFsWrite serves fs/write_text_file under the SAME axis rule as
// handleFsRead (see its doc): chained to the editor when s.fsUpstream is
// set (host axis — the editor's buffer is authoritative for what gets
// written back to it too), local disk otherwise (worktree/container axes,
// unchanged). It returns api.WriteTextFileResponse{} (an empty JSON OBJECT)
// on success either way — the spec's WriteTextFileResponse is
// `"type":"object"` with no null alternative (L0 checklist A1); a bare
// `nil` here used to render as literal JSON `null` via
// jsonrpc.marshalResult, which is schema-invalid.
//
// It funnels through the SAME confineToWorkspace call handleFsRead does (see
// its doc, and fsconfine.go) — one boundary, both directions. The write half
// is where symlink resolution earns its keep: os.WriteFile follows a
// dangling link out of the workspace and CREATES the target, the same
// primitive finding S5 records against copyCredentialFile.
func (s *chatSession) handleFsWrite(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.WriteTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	safePath, cerr := confineToWorkspace(s.workspaceRoot, req.Path)
	if cerr != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "fs/write_text_file: " + cerr.Error()}
	}
	req.Path = safePath
	if s.fsUpstream != nil {
		var resp api.WriteTextFileResponse
		if err := s.fsUpstream.Call(s.ctx, api.ClientMethodFsWriteTextFile, req, &resp); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		}
		return api.WriteTextFileResponse{}, nil
	}
	if err := os.WriteFile(safePath, []byte(req.Content), 0o644); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return api.WriteTextFileResponse{}, nil
}

// --- helpers ---

// rawSessionUpdate extracts a session/update notification's `update` object
// undecoded, so the handler can sniff out-of-SDK variants before the strict
// union decoder sees them.
func rawSessionUpdate(params json.RawMessage) (json.RawMessage, error) {
	var n struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	return n.Update, nil
}

// decodeSessionUpdate parses a session/update notification's params, decoding the
// (SDK-typed-as-interface{}) `update` field into the typed SessionUpdate union.
func decodeSessionUpdate(params json.RawMessage) (*api.SessionUpdate, error) {
	raw, err := rawSessionUpdate(params)
	if err != nil {
		return nil, err
	}
	var upd api.SessionUpdate
	if err := json.Unmarshal(raw, &upd); err != nil {
		return nil, err
	}
	return &upd, nil
}

// sliceLines applies fs/read_text_file's optional 1-based line offset and max
// line count; either being nil means "from the start" / "to the end".
func sliceLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}
