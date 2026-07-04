package acp

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joshgarnett/agent-client-protocol-go/acp/api"

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

// initializeParams is the initialize request body. It exists because the SDK's
// api.InitializeRequest predates the spec's clientInfo field; every other field
// reuses the api types.
type initializeParams struct {
	ProtocolVersion    int                    `json:"protocolVersion"`
	ClientCapabilities api.ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *clientInfoBlock       `json:"clientInfo,omitempty"`
}

type clientInfoBlock struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

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
	tr, err := open(ctx, b.chatArgv(req), req.Env, req.WorkDir)
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
		_ = conn.Notify(api.MethodSessionCancel, api.CancelNotification{SessionId: sessionID})
	}
	// abort cancels the in-flight turn AND tears the transport down. Used on any
	// ctx cancellation — between turns or mid-prompt.
	abort := func() {
		notifyCancel()
		teardown()
	}

	if !sess.send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model}}) {
		abort()
		return ctx.Err()
	}

	type turnResult struct {
		stop string
		err  error
	}
	var (
		turnDone chan turnResult // non-nil while a turn is in flight
		queued   []string        // text messages that arrived mid-turn, in order
		inChan   = in            // nil'd once the caller closes input
	)
	// startTurn WRITES the session/prompt frame synchronously (so a CancelTurn
	// processed later by this loop is guaranteed to reach the agent after the
	// prompt it cancels), then awaits the turn's response off the loop.
	startTurn := func(text string) {
		done := make(chan turnResult, 1)
		turnDone = done
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
			if !sess.send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: res.stop, Model: req.Model}}) {
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

// setup runs the initialize + session/new handshake, returning the new session id.
func (b *ACP) setup(ctx context.Context, conn *jsonrpc.Conn, req agent.ChatRequest) (api.SessionId, error) {
	initParams := initializeParams{
		ProtocolVersion: api.ACPProtocolVersion,
		ClientCapabilities: api.ClientCapabilities{
			// The client owns the cwd, so it is the natural authority for file
			// reads/writes the agent delegates. Terminal is declined (the unstable
			// terminal/* surface is out of scope for this increment).
			Fs: api.FileSystemCapability{ReadTextFile: true, WriteTextFile: true},
		},
		ClientInfo: &clientInfoBlock{Name: clientName, Version: clientVersion},
	}
	var initResp api.InitializeResponse
	if err := conn.Call(ctx, api.MethodInitialize, initParams, &initResp); err != nil {
		return "", err
	}

	cwd := req.WorkDir
	if cwd == "" {
		cwd = "." // spec asks for an absolute cwd; the host supplies one in practice.
	}
	newReq := api.NewSessionRequest{Cwd: cwd, McpServers: mcpServersToACP(req.MCPServers)}
	var newResp api.NewSessionResponse
	if err := conn.Call(ctx, api.MethodSessionNew, newReq, &newResp); err != nil {
		return "", err
	}
	return newResp.SessionId, nil
}

// mcpServersToACP maps the caller-supplied MCP servers onto the session/new
// mcpServers wire shape. Slices stay non-nil (the spec wants arrays, and a nil
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
		out = append(out, api.McpServer{Name: s.Name, Command: s.Command, Args: args, Env: env})
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
		Prompt: []api.PromptRequestPromptElem{
			api.ContentBlock{Type: api.ContentBlockTypeText, Text: &api.ContentBlockText{Text: text}},
		},
	}
	await, err := conn.Go(api.MethodSessionPrompt, promptReq)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		var resp api.PromptResponse
		if aerr := await(ctx, &resp); aerr != nil {
			return "", aerr
		}
		return rawText(resp.StopReason), nil
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
	if method != api.MethodSessionUpdate {
		return
	}
	upd, err := decodeSessionUpdate(params)
	if err != nil {
		warnf("acp: dropping malformed session/update: %v", err)
		return
	}
	for _, ev := range mapSessionUpdate(upd) {
		if !s.send(ev) {
			return
		}
	}
}

// HandleRequest answers an agent→client request. fs I/O and auto-decided
// permissions reply inline (quick, local); a FORWARDED permission replies
// asynchronously — it parks on the caller's answer, and the read loop must stay
// free to keep streaming session/updates while the human decides. Unknown
// methods (e.g. the declined terminal/*) get a JSON-RPC method-not-found error
// rather than crashing.
func (s *chatSession) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.MethodSessionRequestPermission:
		s.handlePermission(params, reply)
	case api.MethodFsReadTextFile:
		reply(s.handleFsRead(params))
	case api.MethodFsWriteTextFile:
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

// handleFsWrite serves fs/write_text_file to the local filesystem. It returns a
// content-less success (JSON null result).
func (s *chatSession) handleFsWrite(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.WriteTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0o644); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return nil, nil
}

// --- helpers ---

// decodeSessionUpdate parses a session/update notification's params, decoding the
// (SDK-typed-as-interface{}) `update` field into the typed SessionUpdate union.
func decodeSessionUpdate(params json.RawMessage) (*api.SessionUpdate, error) {
	var n struct {
		SessionId api.SessionId     `json:"sessionId"`
		Update    api.SessionUpdate `json:"update"`
	}
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	return &n.Update, nil
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
