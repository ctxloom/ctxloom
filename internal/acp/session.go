package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
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
		autoApprove: req.AutoApprove,
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

	// cancelTurn tells the agent to abandon the in-flight turn (best-effort) and
	// tears the transport down. Used on any ctx cancellation — between turns or
	// mid-prompt.
	cancelTurn := func() {
		_ = conn.Notify(api.MethodSessionCancel, api.CancelNotification{SessionId: sessionID})
		teardown()
	}

	if !sess.send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model}}) {
		cancelTurn()
		return ctx.Err()
	}

	for {
		select {
		case <-ctx.Done():
			cancelTurn()
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				// Input closed: no more turns. Tear down cleanly and end.
				teardown()
				return nil
			}
			stop, perr := b.prompt(ctx, conn, sessionID, msg.Text)
			if perr != nil {
				// A prompt failure while ctx is cancelled is a cancellation, not a
				// backend error — cancel the turn and report ctx.Err().
				if ctx.Err() != nil {
					cancelTurn()
					return ctx.Err()
				}
				teardown()
				return perr
			}
			if !sess.send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: stop, Model: req.Model}}) {
				cancelTurn()
				return ctx.Err()
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
	newReq := api.NewSessionRequest{Cwd: cwd, McpServers: []api.McpServer{}}
	var newResp api.NewSessionResponse
	if err := conn.Call(ctx, api.MethodSessionNew, newReq, &newResp); err != nil {
		return "", err
	}
	return newResp.SessionId, nil
}

// prompt sends one user message as a session/prompt turn and blocks until the
// turn ends, returning the stop reason. The turn's session/update stream is
// consumed concurrently by the read loop (chatSession.handleNotification), which
// forwards mapped entries to `out` before this call's response arrives.
func (b *ACP) prompt(ctx context.Context, conn *jsonrpc.Conn, sessionID api.SessionId, text string) (string, error) {
	promptReq := api.PromptRequest{
		SessionId: sessionID,
		Prompt: []api.PromptRequestPromptElem{
			api.ContentBlock{Type: api.ContentBlockTypeText, Text: &api.ContentBlockText{Text: text}},
		},
	}
	var resp api.PromptResponse
	if err := conn.Call(ctx, api.MethodSessionPrompt, promptReq, &resp); err != nil {
		return "", err
	}
	return rawText(resp.StopReason), nil
}

// --- per-conversation callback handler ---

// chatSession is the live conversation state and the rpcHandler for one Chat
// call: it forwards the agent's session/update stream to `out` and answers the
// agent's request callbacks. One per Chat, so `out`/ctx can be captured directly.
type chatSession struct {
	ctx         context.Context
	out         chan<- agent.ChatEvent
	autoApprove bool
	clock       func() time.Time
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

// HandleRequest answers an agent→client request, replying inline (every client
// callback is quick — permission decisioning and local fs I/O). Unknown methods
// (e.g. the declined terminal/*) get a JSON-RPC method-not-found error rather
// than crashing.
func (s *chatSession) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.MethodSessionRequestPermission:
		reply(s.handlePermission(params))
	case api.MethodFsReadTextFile:
		reply(s.handleFsRead(params))
	case api.MethodFsWriteTextFile:
		reply(s.handleFsWrite(params))
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "acp: method not supported: " + method})
	}
}

// handlePermission auto-allows when AutoApprove is set, otherwise rejects —
// mirroring the claude driver, since a structured (non-interactive) chat has no
// human to prompt.
func (s *chatSession) handlePermission(params json.RawMessage) (any, *jsonrpc.Error) {
	var req api.RequestPermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	return decidePermission(req.Options, s.autoApprove), nil
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

