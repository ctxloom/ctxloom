package acp

import (
	"context"
	"encoding/json"
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
func (b *ACP) Chat(parentCtx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
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

	sess := &chatSession{
		ctx:         ctx,
		out:         out,
		autoApprove: req.Permissions.AllowsWithoutPrompt(),
		forward:     req.ForwardPermissions,
		pendingPerm: make(map[string]chan agent.PermissionAnswer),
		clock:       b.clock(),
	}
	conn := jsonrpc.NewConn(ctx, tr.stdout, tr.stdin, jsonrpc.CloserFunc(tr.close), sess)

	// teardown cancels (unblocking any handler parked on an out-send), closes the
	// transport (unblocking a parked reader), then waits for the read loop to exit
	// so nothing races the deferred close(out).
	teardown := func() {
		cancel()
		_ = conn.Close()
		<-conn.Done()
	}

	sessionID, err := b.setup(ctx, conn, req)
	if err != nil {
		teardown()
		return err
	}

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

	if !sess.send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: string(sessionID)}}) {
		abort()
		return ctx.Err()
	}

	type turnResult struct {
		stop string
		err  error
	}
	var (
		turnDone    chan turnResult // non-nil while a turn is in flight
		turnStarted time.Time       // when the in-flight turn's prompt was written
		queued      []string        // text messages that arrived mid-turn, in order
		inChan      = in            // nil'd once the caller closes input
	)
	// startTurn WRITES the session/prompt frame synchronously (so a CancelTurn
	// processed later by this loop is guaranteed to reach the agent after the
	// prompt it cancels), then awaits the turn's response off the loop.
	startTurn := func(text string) {
		done := make(chan turnResult, 1)
		turnDone = done
		turnStarted = sess.clock()
		await, err := b.promptAsync(conn, sessionID, text)
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
			case msg.CancelTurn:
				if turnDone != nil {
					notifyCancel()
				}
			default:
				queued = append(queued, msg.Text)
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

// setup runs the initialize + session/new (or session/load, when resuming)
// handshake, returning the session id the rest of the conversation runs
// under.
func (b *ACP) setup(ctx context.Context, conn *jsonrpc.Conn, req agent.ChatRequest) (api.SessionId, error) {
	initParams := api.InitializeRequest{
		ProtocolVersion: api.ProtocolVersionNumber,
		ClientCapabilities: api.ClientCapabilities{
			// The client owns the cwd, so it is the natural authority for file
			// reads/writes the agent delegates. Terminal is declined (the unstable
			// terminal/* surface is out of scope for this increment).
			Fs: api.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
		ClientInfo: &api.Implementation{Name: clientName, Version: clientVersion},
	}
	var initResp api.InitializeResponse
	if err := conn.Call(ctx, api.AgentMethodInitialize, initParams, &initResp); err != nil {
		return "", err
	}

	cwd := req.WorkDir
	if cwd == "" {
		cwd = "." // spec asks for an absolute cwd; the host supplies one in practice.
	}
	mcpServers := mcpServersToACP(req.MCPServers)

	if req.ResumeSessionID != "" {
		// session/load is capability-gated (unlike session/new): an agent
		// that never advertised it would otherwise silently start a FRESH
		// session under the resumed id's name, discarding continuity the
		// caller explicitly asked for — fail loud instead (adapter/SDK gap,
		// reportable, never worked around by falling back to session/new).
		if !initResp.AgentCapabilities.LoadSession {
			return "", fmt.Errorf("acp: agent does not advertise the loadSession capability; cannot resume session %q (session/new would silently start a fresh session under a resumed id's name)", req.ResumeSessionID)
		}
		loadReq := api.LoadSessionRequest{Cwd: cwd, McpServers: mcpServers, SessionId: api.SessionId(req.ResumeSessionID)}
		// session/load replays the resumed conversation's history as
		// session/update notifications WHILE this call is in flight (per
		// the ACP spec) — sess is already wired as conn's notification
		// handler, so the replay flows to `out` as ordinary ChatEvents
		// before this call returns; the caller's drain loop must already be
		// running (Chat starts it before setup blocks here).
		var loadResp json.RawMessage
		if err := conn.Call(ctx, api.AgentMethodSessionLoad, loadReq, &loadResp); err != nil {
			return "", fmt.Errorf("acp: session/load %q: %w", req.ResumeSessionID, err)
		}
		return api.SessionId(req.ResumeSessionID), nil
	}

	newReq := api.NewSessionRequest{Cwd: cwd, McpServers: mcpServers}
	var newResp api.NewSessionResponse
	if err := conn.Call(ctx, api.AgentMethodSessionNew, newReq, &newResp); err != nil {
		return "", err
	}
	return newResp.SessionId, nil
}

// mcpServersToACP maps the caller-supplied MCP servers onto the session/new
// mcpServers wire shape. ctxloom only ever launches stdio MCP servers, so
// every entry is the union's Stdio variant (the fork's McpServer is now a
// discriminated union of http/sse/acp/stdio, unlike the flat stdio-only
// struct it replaces). Slices stay non-nil (the spec wants arrays, and a nil
// slice marshals as JSON null); env is sorted for a deterministic frame.
func mcpServersToACP(servers []agent.ChatMCPServer) []api.McpServer {
	out := make([]api.McpServer, 0, len(servers))
	for _, s := range servers {
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
	return out
}

// promptAsync sends one user message as a session/prompt turn: the request
// frame is written before it returns, and the returned await blocks until the
// turn ends, yielding the stop reason. The turn's session/update stream is
// consumed concurrently by the read loop (chatSession.HandleNotification),
// which forwards mapped entries to `out` before the response arrives.
func (b *ACP) promptAsync(conn *jsonrpc.Conn, sessionID api.SessionId, text string) (func(context.Context) (string, error), error) {
	promptReq := api.PromptRequest{
		SessionId: sessionID,
		Prompt:    []api.ContentBlock{api.TextBlock(text)},
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

	// forwarded-permission state: each in-flight request parks on its channel
	// until the caller's answer (or input close / ctx death) resolves it.
	permMu      sync.Mutex
	permSeq     int64
	pendingPerm map[string]chan agent.PermissionAnswer
	noInput     bool // input closed: no answers can arrive anymore

	// turn accounting fed by the real usage_update variant and ctxloom's own
	// (renamed, non-colliding) session-info extension — see consumeMetaUpdate.
	// Guarded by metaMu: updates arrive on the read loop while completeMeta
	// runs on the Chat loop.
	metaMu     sync.Mutex
	usage      *api.UsageUpdate // latest usage report (cumulative; freshest wins)
	infoModel  string           // model ctxloom's own agent named in its session-info extension
	infoWindow int              // context window from ctxloom's own session-info extension
}

// send emits one event, stamping a receipt time when the entry lacks one (ACP
// updates carry no per-event timestamp). Returns false if ctx is cancelled first.
func (s *chatSession) send(ev agent.ChatEvent) bool {
	ev = stampTime(ev, s.clock)
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
	// The real usage_update variant, and ctxloom's own renamed session-info
	// extension (see mapping.go's sessionInfoVariant), fold into the turn meta
	// instead of running through mapSessionUpdate as an entry.
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

// consumeMetaUpdate absorbs two session/update variants into the turn
// accounting rather than mapping them to an entry: the real spec usage_update
// (now decoded straight into api.UsageUpdate), and ctxloom's own session-info
// extension — emitted under a ctxloom-scoped name, NOT the spec's
// session_info_update, which means something unrelated (session
// title/timestamp metadata) as of schema-v1.19.0; see the emitter's doc
// comment (internal/acpagent/wire.go) and mapping.go's sessionInfoVariant.
// Returns true when the update was one of those (there is no entry to emit);
// a malformed frame of a recognized variant is warned and dropped, never
// crashing the stream.
func (s *chatSession) consumeMetaUpdate(raw json.RawMessage) bool {
	switch updateDiscriminator(raw) {
	case usageUpdateVariant:
		var u api.UsageUpdate
		if err := json.Unmarshal(raw, &u); err != nil {
			warnf("acp: dropping malformed usage_update: %v", err)
			return true
		}
		s.metaMu.Lock()
		s.usage = &u
		s.metaMu.Unlock()
		return true
	case sessionInfoVariant:
		var info sessionInfoWire
		if err := json.Unmarshal(raw, &info); err != nil {
			warnf("acp: dropping malformed session_info_update: %v", err)
			return true
		}
		s.metaMu.Lock()
		if info.Model != "" {
			s.infoModel = info.Model
		}
		if info.ContextWindow > 0 {
			s.infoWindow = info.ContextWindow
		}
		s.metaMu.Unlock()
		return true
	}
	return false
}

// completeMeta assembles one turn's completion accounting: the wire-sourced
// stop reason; the latest usage/session-info the agent reported (both are
// cumulative session state, so the freshest value stands across turns); the
// requested model unless the agent named its own; and the self-measured
// wall-clock duration (the protocol carries no timing — see mapping.go on
// what ACP v1 does and does not deliver).
func (s *chatSession) completeMeta(stop string, started time.Time, requestedModel string) *agent.TurnMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m := &agent.TurnMeta{
		StopReason:    stop,
		Model:         requestedModel,
		ContextWindow: s.infoWindow,
		DurationMs:    int(s.clock().Sub(started).Milliseconds()),
	}
	if s.infoModel != "" {
		m.Model = s.infoModel
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
	return ch, id
}

// forwardPermission emits the request upstream and replies with the caller's
// decision. A closed answer channel (input closed), a dismissed answer (empty
// OptionID), or ctx death all resolve as a "cancelled" outcome — the safe
// no-op that neither approves nor commits a remembered rejection.
func (s *chatSession) forwardPermission(id string, ch chan agent.PermissionAnswer, req *api.RequestPermissionRequest, reply func(any, *jsonrpc.Error)) {
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
	defer s.permMu.Unlock()
	s.noInput = true
	for id, ch := range s.pendingPerm {
		close(ch)
		delete(s.pendingPerm, id)
	}
}

// handleFsRead serves fs/read_text_file from the local filesystem, honoring the
// optional 1-based line offset and line limit.
func (s *chatSession) handleFsRead(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.ReadTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return api.ReadTextFileResponse{Content: sliceLines(string(data), req.Line, req.Limit)}, nil
}

// handleFsWrite serves fs/write_text_file to the local filesystem. It returns
// api.WriteTextFileResponse{} (an empty JSON OBJECT) on success — the spec's
// WriteTextFileResponse is `"type":"object"` with no null alternative (L0
// checklist A1); a bare `nil` here used to render as literal JSON `null` via
// jsonrpc.marshalResult, which is schema-invalid.
func (s *chatSession) handleFsWrite(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.WriteTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0o644); err != nil {
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

// stampTime records receipt time on an entry event that arrived without one.
func stampTime(ev agent.ChatEvent, now func() time.Time) agent.ChatEvent {
	if ev.Entry != nil && ev.Entry.Timestamp.IsZero() {
		ev.Entry.Timestamp = now()
	}
	return ev
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
