package mock_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// fakeHost is a session endpoint serving only the permission host, behind
// a bearer: it records each call and answers with a fixed text.
type fakeHost struct {
	mu     sync.Mutex
	calls  []string
	answer string
}

func (h *fakeHost) serve(t *testing.T) sessions.Endpoint {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fake-endpoint", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: engine.PermissionHostTool, InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			h.mu.Lock()
			h.calls = append(h.calls, string(req.Params.Arguments))
			h.mu.Unlock()
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: h.answer}}}, nil
		})
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return sessions.Endpoint{URL: srv.URL + "/mcp", Credential: "tok"}
}

func (h *fakeHost) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// askTurn runs one mock:ask turn for Bash {"command":"ls"} under the
// approver, with the hook commands delivered as permission_ask hooks, and
// returns what the turn relayed.
func askTurn(t *testing.T, approver engine.Approver, ep sessions.Endpoint, hookCommands ...string) []agent.ChatEvent {
	t.Helper()
	var hooks []wire.Hook
	for _, c := range hookCommands {
		hooks = append(hooks, wire.Hook{Type: "command", Command: c})
	}
	return askTurnWith(t, approver, ep, hooks)
}

// askTurnWith is askTurn with the permission_ask hooks given whole.
func askTurnWith(t *testing.T, approver engine.Approver, ep sessions.Endpoint, ask []wire.Hook) []agent.ChatEvent {
	t.Helper()
	dir := t.TempDir()
	hooks := wire.UnifiedHooks{PermissionAsk: ask}
	raw, err := json.Marshal(hooks)
	require.NoError(t, err)
	hooksFile := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksFile, raw, 0o600))

	inst, err := mock.New().Instance(engine.Session{
		Mode: engine.Structured, WorkDir: dir, MCP: ep,
		Permission: engine.PermissionPolicy{Approver: approver},
	})
	require.NoError(t, err)
	out := make(chan engine.Event, 64)
	ex := engine.Exec{Args: []string{mock.HooksFlag, hooksFile}, WorkDir: dir, Env: map[string]string{}}
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.Ask("Bash", `{"command":"ls"}`)}, out)
	require.NoError(t, err)
	close(out)
	var evs []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		evs = append(evs, ce)
	}
	return evs
}

// callOutcome is what the turn reported for the asked call: its result
// entry and its denial, if any, and the completion's denials.
func callOutcome(t *testing.T, evs []agent.ChatEvent) (use, result *agent.SessionEntry, denied *agent.PermissionDenial, meta *agent.TurnMeta) {
	t.Helper()
	for _, ev := range evs {
		switch {
		case ev.Entry != nil && ev.Entry.ToolCallID == mock.AskCallID && ev.Entry.Type == agent.EntryTypeToolUse:
			use = ev.Entry
		case ev.Entry != nil && ev.Entry.ToolCallID == mock.AskCallID && ev.Entry.Type == agent.EntryTypeToolResult:
			result = ev.Entry
		case ev.Denied != nil:
			denied = ev.Denied
		case ev.Complete != nil:
			meta = ev.Complete
		}
	}
	require.NotNil(t, use, "the call went out on the stream")
	require.NotNil(t, result, "the call has a result")
	require.NotNil(t, meta)
	return use, result, denied, meta
}

// TestMockAsk_TheHooksAnswerDecides: the hook's answer wins over the
// host's — an allow runs the call, a deny is the turn's denial.
func TestMockAsk_TheHooksAnswerDecides(t *testing.T) {
	host := &fakeHost{answer: `{"allow":false,"message":"the host never decides"}`}
	ep := host.serve(t)

	use, result, denied, meta := callOutcome(t, askTurn(t, engine.ApproverHuman, ep, `cat >/dev/null; printf '{"allow":true}'`))
	assert.Equal(t, "Bash", use.ToolName)
	assert.JSONEq(t, `{"command":"ls"}`, string(use.ToolInput))
	assert.False(t, result.IsError, "allowed: the call ran")
	assert.Nil(t, denied)
	assert.Empty(t, meta.Denials)

	_, result, denied, meta = callOutcome(t, askTurn(t, engine.ApproverHuman, ep, `cat >/dev/null; printf '{"allow":false,"message":"not today"}'`))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Equal(t, "not today", denied.Reason)
	assert.Equal(t, []agent.PermissionDenial{*denied}, meta.Denials)
}

// TestMockAsk_WithNoHookAnswerTheHostDecides: a hook that writes nothing
// decides nothing; the held host's answer stands. The host is handed the
// call WITH its id; the hook's payload carries none (claude's permission
// ask names no call), so the runner must anchor it on the host.
func TestMockAsk_WithNoHookAnswerTheHostDecides(t *testing.T) {
	host := &fakeHost{answer: `{"allow":false,"message":"ctxloom: the approval hook did not answer"}`}
	ep := host.serve(t)
	payload := filepath.Join(t.TempDir(), "payload.json")

	_, _, denied, _ := callOutcome(t, askTurn(t, engine.ApproverHuman, ep, `cat > `+payload))
	require.NotNil(t, denied)
	assert.Equal(t, "ctxloom: the approval hook did not answer", denied.Reason)

	calls := host.seen()
	require.Len(t, calls, 1)
	assert.JSONEq(t, `{"tool":"Bash","input":{"command":"ls"},"tool_use_id":"`+mock.AskCallID+`"}`, calls[0])
	body, err := os.ReadFile(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool":"Bash","input":{"command":"ls"},"tool_use_id":""}`, string(body))
}

// TestMockAsk_NobodyToAskIsADenial: with any approver but the human the
// call is denied at once — neither the host nor a hook is consulted.
func TestMockAsk_NobodyToAskIsADenial(t *testing.T) {
	host := &fakeHost{answer: `{"allow":true}`}
	ep := host.serve(t)
	marker := filepath.Join(t.TempDir(), "ran")

	_, result, denied, meta := callOutcome(t, askTurn(t, engine.ApproverNone, ep, `touch `+marker+`; printf '{"allow":true}'`))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Len(t, meta.Denials, 1)
	assert.Empty(t, host.seen(), "nobody to ask: the host is not called")
	assert.NoFileExists(t, marker, "nobody to ask: no hook runs")
}

// TestMockAsk_AHookWhoseMatcherExcludesTheToolDecidesNothing: a hook is
// consulted only for the tools its matcher admits; one matched to another
// tool decides nothing, and the host's answer stands.
func TestMockAsk_AHookWhoseMatcherExcludesTheToolDecidesNothing(t *testing.T) {
	host := &fakeHost{answer: `{"allow":false,"message":"held and denied"}`}
	ep := host.serve(t)
	_, _, denied, _ := callOutcome(t, askTurnWith(t, engine.ApproverHuman, ep, []wire.Hook{
		{Type: "command", Matcher: "Write", Command: `cat >/dev/null; printf '{"allow":true}'`},
	}))
	require.NotNil(t, denied, "the Write hook does not decide a Bash call")
	assert.Equal(t, "held and denied", denied.Reason)
}
