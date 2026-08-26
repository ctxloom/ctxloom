package acpagent

// L1 — real process boundary loopback suite (ACP H1 conformance harness).
//
// server_test.go drives Serve() IN-PROCESS via startServer's io.Pipe, and
// internal/acp/fakeagent_test.go drives the CLIENT half in-process against a
// scripted fake agent. Neither ever crosses a real OS process boundary. This
// file joins them: it spawns cmd/acpl1harness — a small binary that calls the
// EXACT SAME production entrypoint (acpagent.Serve) `ctxloom acp` calls, with
// a deterministic scripted engine behind it instead of a real LLM (see that
// package's doc comment for the full rationale on why it isn't literally
// `ctxloom acp` against a real backend) — and drives it over REAL stdio pipes
// using a test client built on the PRODUCTION internal/acp/jsonrpc codec plus
// the vendored SDK's api types, exactly as a real ACP client (Zed, etc.)
// would.
//
// Every assertion inspects real frame/field content (never just "no error"):
// this codebase's characteristic bug is a silent no-op (exit 0, success
// reply, zero meaningful bytes), and an exit-code-only gate is blind to it.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// Sentinel prompt texts the harness engine (cmd/acpl1harness/engine.go)
// scripts on. Deliberately duplicated here rather than imported (a `main`
// package can't be imported anyway, and — mirroring fakeagent_test.go's
// documented rationale for keeping its rpcMessage type independent of the
// production codec — keeping the driver's expectations textually separate
// from the engine's implementation means a copy-paste sync bug can't hide by
// construction). Keep in sync with cmd/acpl1harness/engine.go by hand.
const (
	l1SentinelCancelMe     = "L1_CANCEL_ME"
	l1SentinelRaceComplete = "L1_RACE_COMPLETE"
	l1SentinelPermission   = "L1_PERMISSION"
	l1SentinelFail         = "L1_FAIL"
	l1SentinelTerminal     = "L1_TERMINAL"
	l1FailMessage          = "l1 harness: deliberate failure for conformance testing"
	l1ReplayUser           = "earlier question (l1 harness replay)"
	l1ReplayAssistant      = "earlier answer (l1 harness replay)"
)

// --- one-time binary build ---

var (
	l1BuildOnce sync.Once
	l1BinPath   string
	l1BuildErr  error
	l1BuildDir  string
)

// TestMain builds the acpl1harness binary once for the whole package's test
// run (a fresh `go build` per test would be needlessly slow) and cleans up
// its temp dir afterward.
func TestMain(m *testing.M) {
	code := m.Run()
	if l1BuildDir != "" {
		_ = os.RemoveAll(l1BuildDir)
	}
	os.Exit(code)
}

// l1Binary builds (once) and returns the path to the acpl1harness binary.
func l1Binary(t *testing.T) string {
	t.Helper()
	l1BuildOnce.Do(func() {
		root := l1ModuleRoot()
		dir, err := os.MkdirTemp("", "acpl1harness-")
		if err != nil {
			l1BuildErr = err
			return
		}
		l1BuildDir = dir
		bin := filepath.Join(dir, "acpl1harness")
		// A direct exec.Command — NOT the `just` task runner — is
		// deliberate: this is a subprocess spawned by a running Go test
		// binary, not a Bash tool invocation, so it is not what the repo's
		// "build through just, not bare go build" guidance is warning
		// against (that guidance is about matching CI's build flags for the
		// SHIPPED ctxloom binary; this is a disposable test fixture that
		// carries no version stamp and is never released).
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/acpl1harness")
		cmd.Dir = root
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			l1BuildErr = errWithOutput(err, out.String())
			return
		}
		l1BinPath = bin
	})
	require.NoError(t, l1BuildErr, "building cmd/acpl1harness")
	return l1BinPath
}

func errWithOutput(err error, out string) error {
	return &buildError{err: err, out: out}
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }
func (e *buildError) Unwrap() error { return e.err }

// l1ModuleRoot walks up from the current test file's directory to find go.mod.
func l1ModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// --- test client: a real subprocess driven over the production jsonrpc codec ---

// l1Frame is one captured inbound notification (session/update) or request
// (session/request_permission) from the spawned agent.
type l1Frame struct {
	Method string
	Params json.RawMessage
	// reply is set only for captured REQUESTS (session/request_permission);
	// the test calls it to answer, exactly like a real ACP client would.
	reply func(any, *jsonrpc.Error)
}

// l1Handler is the jsonrpc.Handler for the test's connection to the spawned
// agent: it captures session/update notifications, session/request_permission
// requests, and terminal/* requests (B1, gap G6) for the driving test to
// inspect and answer.
type l1Handler struct {
	updates  chan l1Frame
	permReqs chan l1Frame
	termReqs chan l1Frame
}

func newL1Handler() *l1Handler {
	return &l1Handler{
		updates:  make(chan l1Frame, 64),
		permReqs: make(chan l1Frame, 8),
		termReqs: make(chan l1Frame, 8),
	}
}

func (h *l1Handler) HandleRequest(_ context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.ClientMethodSessionRequestPermission:
		h.permReqs <- l1Frame{Method: method, Params: params, reply: reply}
	case api.ClientMethodTerminalCreate, api.ClientMethodTerminalOutput,
		api.ClientMethodTerminalWaitForExit, api.ClientMethodTerminalKill, api.ClientMethodTerminalRelease:
		h.termReqs <- l1Frame{Method: method, Params: params, reply: reply}
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "l1 test client: unsupported method " + method})
	}
}

func (h *l1Handler) HandleNotification(_ context.Context, method string, params json.RawMessage) {
	if method == api.ClientMethodSessionUpdate {
		h.updates <- l1Frame{Method: method, Params: params}
	}
}

// l1Proc is a running acpl1harness subprocess plus the production jsonrpc.Conn
// driving it over its real stdin/stdout pipes.
type l1Proc struct {
	t       *testing.T
	cmd     *exec.Cmd
	conn    *jsonrpc.Conn
	handler *l1Handler
	stderr  *bytes.Buffer
}

// startL1Proc spawns a fresh acpl1harness process and wires the production
// jsonrpc codec to its real stdio.
func startL1Proc(t *testing.T) *l1Proc {
	t.Helper()
	bin := l1Binary(t)

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start(), "spawning acpl1harness")

	h := newL1Handler()
	ctx, cancel := context.WithCancel(context.Background())
	conn := jsonrpc.NewConn(stdout, stdin, jsonrpc.CloserFunc(stdin.Close), h)
	conn.Start(ctx)

	p := &l1Proc{t: t, cmd: cmd, conn: conn, handler: h, stderr: &stderr}
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if t.Failed() && stderr.Len() > 0 {
			t.Logf("acpl1harness stderr:\n%s", stderr.String())
		}
	})
	return p
}

func (p *l1Proc) initialize() api.InitializeResponse {
	p.t.Helper()
	var resp api.InitializeResponse
	err := p.conn.Call(context.Background(), api.AgentMethodInitialize,
		map[string]any{"protocolVersion": api.ProtocolVersionNumber, "clientCapabilities": map[string]any{}}, &resp)
	require.NoError(p.t, err, "initialize")
	return resp
}

// initializeWithTerminal is initialize() advertising
// clientCapabilities.terminal: true (B1, gap G6) — a real editor sets this
// so the agent may honestly ask its engine's client-role driver to broker
// terminal/* to it; see internal/acp/session.go's setup doc comment for
// exactly what this promises.
func (p *l1Proc) initializeWithTerminal() api.InitializeResponse {
	p.t.Helper()
	var resp api.InitializeResponse
	err := p.conn.Call(context.Background(), api.AgentMethodInitialize,
		map[string]any{"protocolVersion": api.ProtocolVersionNumber, "clientCapabilities": map[string]any{"terminal": true}}, &resp)
	require.NoError(p.t, err, "initialize")
	return resp
}

// newSession runs session/new and returns the minted session id.
func (p *l1Proc) newSession(cwd string) api.SessionId {
	p.t.Helper()
	var resp api.NewSessionResponse
	err := p.conn.Call(context.Background(), api.AgentMethodSessionNew,
		map[string]any{"cwd": cwd, "mcpServers": []any{}}, &resp)
	require.NoError(p.t, err, "session/new")
	require.NotEmpty(p.t, resp.SessionId, "session/new must mint a real session id")
	return resp.SessionId
}

// loadSession runs session/load and returns the raw result plus every
// session/update notification that had already arrived by the time the
// response landed (the spec's required replay-before-response order — see
// TestL1_SessionLoad_ReplaysBeforeResponse).
func (p *l1Proc) loadSession(harp, cwd string) (json.RawMessage, []l1Frame) {
	p.t.Helper()
	var raw json.RawMessage
	err := p.conn.Call(context.Background(), api.AgentMethodSessionLoad,
		map[string]any{"sessionId": harp, "cwd": cwd, "mcpServers": []any{}}, &raw)
	require.NoError(p.t, err, "session/load")
	return raw, p.drainUpdates()
}

// drainUpdates non-blockingly collects every update captured so far.
func (p *l1Proc) drainUpdates() []l1Frame {
	var out []l1Frame
	for {
		select {
		case f := <-p.handler.updates:
			out = append(out, f)
		default:
			return out
		}
	}
}

// promptAsync writes a session/prompt request (guaranteed written before this
// call returns — see jsonrpc.Conn.Go) and returns an await func for its
// response, so the caller can order a subsequent session/cancel notification
// strictly after the prompt.
func (p *l1Proc) promptAsync(sid api.SessionId, text string) func(context.Context) (api.PromptResponse, *jsonrpc.Error) {
	p.t.Helper()
	req := api.PromptRequest{
		SessionId: sid,
		Prompt:    []api.ContentBlock{api.TextBlock(text)},
	}
	await, err := p.conn.Go(api.AgentMethodSessionPrompt, req)
	require.NoError(p.t, err, "writing session/prompt")
	return func(ctx context.Context) (api.PromptResponse, *jsonrpc.Error) {
		var resp api.PromptResponse
		aerr := await(ctx, &resp)
		if aerr == nil {
			return resp, nil
		}
		rerr, ok := aerr.(*jsonrpc.Error)
		require.True(p.t, ok, "session/prompt failed with a non-RPC error: %v", aerr)
		return resp, rerr
	}
}

func (p *l1Proc) cancel(sid api.SessionId) {
	p.t.Helper()
	require.NoError(p.t, p.conn.Notify(api.AgentMethodSessionCancel, api.CancelNotification{SessionId: sid}))
}

func (p *l1Proc) waitUpdate() l1Frame {
	p.t.Helper()
	select {
	case f := <-p.handler.updates:
		return f
	case <-time.After(10 * time.Second):
		p.t.Fatal("timed out waiting for a session/update")
		return l1Frame{}
	}
}

func (p *l1Proc) waitPermRequest() l1Frame {
	p.t.Helper()
	select {
	case f := <-p.handler.permReqs:
		return f
	case <-time.After(10 * time.Second):
		p.t.Fatal("timed out waiting for a session/request_permission")
		return l1Frame{}
	}
}

// waitTerminalRequest blocks for one forwarded terminal/* request (B1, gap G6).
func (p *l1Proc) waitTerminalRequest() l1Frame {
	p.t.Helper()
	select {
	case f := <-p.handler.termReqs:
		return f
	case <-time.After(10 * time.Second):
		p.t.Fatal("timed out waiting for a terminal/* request")
		return l1Frame{}
	}
}

// updateKind decodes just the session/update discriminator + a text/content
// summary sufficient for payload assertions.
type updateSummary struct {
	SessionId string `json:"sessionId"`
	Update    struct {
		Kind       string `json:"sessionUpdate"`
		ToolCallId string `json:"toolCallId"`
		Content    struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"update"`
}

func decodeUpdate(t *testing.T, f l1Frame) updateSummary {
	t.Helper()
	var u updateSummary
	require.NoError(t, json.Unmarshal(f.Params, &u))
	return u
}

// --- lifecycle semantic tests ---

// TestL1_FullTurn_RealProcessBoundary: initialize -> session/new ->
// session/prompt across a real spawned OS process, asserting the exact
// streamed content and stop reason — the baseline proof the two halves join
// correctly over real stdio (never exercised together before this harness).
func TestL1_FullTurn_RealProcessBoundary(t *testing.T) {
	p := startL1Proc(t)
	initResp := p.initialize()
	assert.EqualValues(t, 1, initResp.ProtocolVersion, "protocol version 1 over the real wire")
	assert.True(t, initResp.AgentCapabilities.LoadSession, "agent capabilities must actually cross the process boundary")

	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, "hello over a real pipe")
	upd := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "agent_message_chunk", upd.Update.Kind)
	assert.Equal(t, "echo: hello over a real pipe", upd.Update.Content.Text, "the exact echoed text must survive process + JSON-RPC framing")

	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// TestL1_SessionLoad_ReplaysBeforeResponse: session/load's recorded history
// notifications arrive BEFORE the session/load response resolves — the
// spec's required ordering — with the exact replayed content, across a real
// process boundary.
func TestL1_SessionLoad_ReplaysBeforeResponse(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()

	_, updates := p.loadSession("any-harp-name", t.TempDir())
	require.Len(t, updates, 2, "both recorded entries must replay before the session/load response")
	first := decodeUpdate(t, updates[0])
	second := decodeUpdate(t, updates[1])
	assert.Equal(t, "user_message_chunk", first.Update.Kind)
	assert.Equal(t, l1ReplayUser, first.Update.Content.Text)
	assert.Equal(t, "agent_message_chunk", second.Update.Kind)
	assert.Equal(t, l1ReplayAssistant, second.Update.Content.Text)

	// The resumed session is still usable under its requested harp id.
	await := p.promptAsync(api.SessionId("any-harp-name"), "still alive?")
	p.waitUpdate()
	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// TestL1_CancelMidTurn_ResolvesCancelled: session/cancel on an in-flight turn
// resolves the prompt with stopReason "cancelled" and leaves the session
// usable for the next turn — the ordinary (non-racing) cancel path, over a
// real process boundary.
func TestL1_CancelMidTurn_ResolvesCancelled(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelCancelMe)
	upd := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "waiting for cancel", upd.Update.Content.Text, "the turn is confirmed in flight before cancelling")

	p.cancel(sid)
	resp, rerr := await(context.Background())
	require.Nil(t, rerr, "cancel must resolve the prompt, not error it")
	assert.EqualValues(t, api.StopReasonCancelled, resp.StopReason)

	// The session survived: a normal follow-up turn still works.
	await2 := p.promptAsync(sid, "still here")
	upd2 := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "echo: still here", upd2.Update.Content.Text)
	resp2, rerr2 := await2(context.Background())
	require.Nil(t, rerr2)
	assert.EqualValues(t, api.StopReasonEndTurn, resp2.StopReason)
}

// TestL1_CancelRacesEngineCompletion_StillResolvesCancelled pins server.go's
// stopReason() (lines ~598-606): even when the engine itself reports
// "end_turn" after having already received the forwarded cancel (it "raced"
// the cancel and finished anyway), the client-visible outcome MUST be
// "cancelled" because session/cancel arrived for this turn. This is the
// exact scenario the task brief calls out by name.
func TestL1_CancelRacesEngineCompletion_StillResolvesCancelled(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelRaceComplete)
	upd := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "finishing before any cancel is read", upd.Update.Content.Text)

	p.cancel(sid)
	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonCancelled, resp.StopReason,
		"the engine self-reported end_turn after receiving the forwarded cancel; the server MUST override it to cancelled")
}

// TestL1_PermissionForwarding_Selected: an engine permission request forwards
// to the client as session/request_permission (with the announced tool
// call's id), and the client's selected option rides back into the engine —
// proven by the engine echoing the exact option id it received, over a real
// process boundary.
func TestL1_PermissionForwarding_Selected(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelPermission)
	toolCall := decodeUpdate(t, p.waitUpdate())
	require.Equal(t, "tool_call", toolCall.Update.Kind)
	require.NotEmpty(t, toolCall.Update.ToolCallId)

	permReq := p.waitPermRequest()
	var pr struct {
		SessionId string `json:"sessionId"`
		Options   []struct {
			OptionId string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
		ToolCall struct {
			ToolCallId string `json:"toolCallId"`
		} `json:"toolCall"`
	}
	require.NoError(t, json.Unmarshal(permReq.Params, &pr))
	assert.Equal(t, string(sid), pr.SessionId)
	require.Len(t, pr.Options, 2)
	assert.Equal(t, "allow", pr.Options[0].OptionId)
	assert.Equal(t, toolCall.Update.ToolCallId, pr.ToolCall.ToolCallId, "the permission references the SAME announced tool call")

	permReq.reply(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow"}}, nil)

	answerEntry := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "permission: allow", answerEntry.Update.Content.Text, "the selected option id round-tripped through the engine verbatim")

	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// TestL1_PermissionForwarding_Dismissed: when the client answers a permission
// request with a "cancelled" outcome (or an error — see
// server_test.go's ClientError variant for the in-process twin), the engine
// receives a DISMISSED answer (empty option id), never a false approval.
func TestL1_PermissionForwarding_Dismissed(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelPermission)
	p.waitUpdate() // tool_call
	permReq := p.waitPermRequest()

	permReq.reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil)

	answerEntry := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "permission: dismissed", answerEntry.Update.Content.Text, "a cancelled outcome must dismiss, never silently approve")

	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// TestL1_EngineError_MessageAssertedVerbatim: a fatal engine error surfaces
// as a JSON-RPC error whose message is asserted VERBATIM (not merely
// non-empty) — error message QUALITY across a real process boundary.
func TestL1_EngineError_MessageAssertedVerbatim(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelFail)
	resp, rerr := await(context.Background())
	require.NotNil(t, rerr, "a fatal engine error must surface as a JSON-RPC error")
	assert.Equal(t, jsonrpc.CodeInternalError, rerr.Code)
	assert.Equal(t, l1FailMessage, rerr.Message, "the engine's error text must reach the client VERBATIM, not a generic wrapper")
	assert.Empty(t, resp.StopReason, "an errored response carries no stop reason")
}

// TestL1_OneReplyPerRequest_ConcurrentPromptIsRejectedNotQueued: while a turn
// is in flight, a second session/prompt on the SAME session gets EXACTLY ONE
// reply — a verbatim error, not silently queued and not a second success —
// proving the server's reply-exactly-once contract (jsonrpc.serveRequest's
// sync.Once-guarded reply, combined with handlePrompt's inTurn guard) holds
// across a real process boundary. The in-flight turn is then cancelled to
// leave the session clean.
func TestL1_OneReplyPerRequest_ConcurrentPromptIsRejectedNotQueued(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	firstAwait := p.promptAsync(sid, l1SentinelCancelMe)
	p.waitUpdate() // "waiting for cancel" — the first turn is confirmed in flight

	secondAwait := p.promptAsync(sid, "should be rejected")
	_, secondErr := secondAwait(context.Background())
	require.NotNil(t, secondErr, "a concurrent second prompt on the same session must be rejected, not queued")
	assert.Equal(t, "a turn is already in flight for this session", secondErr.Message)

	p.cancel(sid)
	firstResp, firstErr := firstAwait(context.Background())
	require.Nil(t, firstErr)
	assert.EqualValues(t, api.StopReasonCancelled, firstResp.StopReason, "the FIRST (legitimate) turn still resolves normally")
}

// TestL1_TerminalForwarding_RealProcessBoundary (B1, gap G6): an engine
// terminal/create request forwards to the client as a REAL terminal/create
// JSON-RPC request — with THIS session's real editor-facing id substituted
// for the engine's own opaque one (proving terminalParamsWithSession, the
// exact analog of permissionRequestWire's sess.id substitution) — and the
// editor's real answer rides back into the engine verbatim, across a real
// spawned OS process boundary in both directions. This is the headline B1
// proof: ctxloom brokers terminal/*, it never fabricates or drops it.
func TestL1_TerminalForwarding_RealProcessBoundary(t *testing.T) {
	p := startL1Proc(t)
	p.initializeWithTerminal()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelTerminal)

	termReq := p.waitTerminalRequest()
	assert.Equal(t, api.ClientMethodTerminalCreate, termReq.Method)
	var params struct {
		SessionId string `json:"sessionId"`
		Command   string `json:"command"`
		Args      []string
	}
	require.NoError(t, json.Unmarshal(termReq.Params, &params))
	assert.Equal(t, string(sid), params.SessionId, "the agent must substitute ITS OWN editor-facing session id, not the engine's opaque one")
	assert.Equal(t, "echo", params.Command, "the op's real body must cross the process boundary verbatim")
	assert.Equal(t, []string{"hi"}, params.Args)

	termReq.reply(map[string]any{"terminalId": "real-editor-terminal-42"}, nil)

	answerEntry := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, `terminal result: {"terminalId":"real-editor-terminal-42"}`, answerEntry.Update.Content.Text,
		"the editor's real answer bytes must round-trip back into the engine verbatim")

	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// TestL1_TerminalForwarding_EditorDeclines: when the connected editor answers
// a forwarded terminal/create with a JSON-RPC error, the engine receives that
// SPECIFIC reason as a TerminalResponse.Error — never a silent drop, a hang,
// or a fabricated success (this codebase's signature failure mode).
func TestL1_TerminalForwarding_EditorDeclines(t *testing.T) {
	p := startL1Proc(t)
	p.initializeWithTerminal()
	sid := p.newSession(t.TempDir())

	await := p.promptAsync(sid, l1SentinelTerminal)
	termReq := p.waitTerminalRequest()
	termReq.reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "editor: terminal not actually available"})

	answerEntry := decodeUpdate(t, p.waitUpdate())
	assert.Contains(t, answerEntry.Update.Content.Text, "terminal error:")
	assert.Contains(t, answerEntry.Update.Content.Text, "editor: terminal not actually available",
		"the editor's real decline reason must reach the engine, not a generic failure")

	resp, rerr := await(context.Background())
	require.Nil(t, rerr)
	assert.EqualValues(t, api.StopReasonEndTurn, resp.StopReason)
}

// --- malformed-input / capability-mismatch (flow-batch review gap) ---
//
// Every test above drives a WELL-FORMED request across the real process
// boundary. This suite had none exercising a malformed frame, an unknown
// method, or a request against an unknown session — the review's own
// pattern for a batch's flow-level test ("a journey with no malformed-input
// scenario is a serious gap") applied here as much as to any acceptance
// journey. The three below close it at the SAME real-process level the rest
// of this file operates at, not just at the in-process unit level
// (jsonrpc_test.go's TestServeInboundRequest_UnknownMethod already covered
// the codec in isolation; this proves the whole spawned production stack
// gives the identical, correct answer end to end).

// TestL1_UnknownMethod_RealProcessBoundary: a method the agent role does not
// implement gets a real -32601 (Method Not Found) JSON-RPC error back over
// the actual process boundary — not a hang, not a silently dropped frame,
// not a crash that takes cmd/acpl1harness down (which would fail every
// later assertion in this same test via a broken pipe, so a hang/crash here
// is not a possible false negative).
func TestL1_UnknownMethod_RealProcessBoundary(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()

	var resp json.RawMessage
	err := p.conn.Call(context.Background(), "session/no_such_method", map[string]any{}, &resp)
	require.Error(t, err, "an unknown method must be answered with an error, not silently succeed or hang")
	rerr, ok := err.(*jsonrpc.Error)
	require.True(t, ok, "expected a *jsonrpc.Error, got %T: %v", err, err)
	assert.Equal(t, jsonrpc.CodeMethodNotFound, rerr.Code)

	// The connection must survive an unknown method: a real request right
	// after still works normally.
	sid := p.newSession(t.TempDir())
	assert.NotEmpty(t, sid)
}

// TestL1_SessionPrompt_UnknownSessionId_RealProcessBoundary: a session/prompt
// naming a session id nothing ever minted gets a real error naming the
// unknown id — not a hang (there is no session to register a turn against,
// so a hang here would mean the request silently vanished) and not a
// fabricated success.
func TestL1_SessionPrompt_UnknownSessionId_RealProcessBoundary(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()

	await := p.promptAsync(api.SessionId("no-such-session-ever-minted"), "hello")
	_, rerr := await(context.Background())
	require.NotNil(t, rerr, "a prompt against an unknown session id must error, not hang or silently succeed")
	assert.Contains(t, rerr.Message, "no-such-session-ever-minted", "the error must name the actual unknown id, not a generic failure")
}

// TestL1_SessionPrompt_MalformedParams_RealProcessBoundary: a session/prompt
// whose params are valid JSON but the WRONG SHAPE (prompt as a bare string
// instead of the required content-block array) is refused as invalid
// params, across the real process boundary, and the connection survives to
// serve a well-formed request afterward.
func TestL1_SessionPrompt_MalformedParams_RealProcessBoundary(t *testing.T) {
	p := startL1Proc(t)
	p.initialize()
	sid := p.newSession(t.TempDir())

	var resp json.RawMessage
	err := p.conn.Call(context.Background(), api.AgentMethodSessionPrompt,
		map[string]any{"sessionId": string(sid), "prompt": "not an array, a bare string"}, &resp)
	require.Error(t, err, "a wrong-shaped prompt payload must be refused, not silently accepted or hung on")
	rerr, ok := err.(*jsonrpc.Error)
	require.True(t, ok, "expected a *jsonrpc.Error, got %T: %v", err, err)
	assert.Equal(t, jsonrpc.CodeInvalidParams, rerr.Code)

	// The session survives a malformed request: a well-formed prompt after
	// it still runs a normal turn.
	await := p.promptAsync(sid, "still alive after the malformed one")
	upd := decodeUpdate(t, p.waitUpdate())
	assert.Equal(t, "echo: still alive after the malformed one", upd.Update.Content.Text)
	goodResp, goodErr := await(context.Background())
	require.Nil(t, goodErr)
	assert.EqualValues(t, api.StopReasonEndTurn, goodResp.StopReason)
}
