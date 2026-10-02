package mock_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// askTurn runs one mock:ask turn for Bash {"command":"ls"} under the
// approver, with the hook commands delivered as permission_ask hooks, and
// returns what the turn relayed.
func askTurn(t *testing.T, approver engine.Approver, hookCommands ...string) []agent.ChatEvent {
	t.Helper()
	var hooks []wire.Hook
	for _, c := range hookCommands {
		hooks = append(hooks, wire.Hook{Type: "command", Command: c})
	}
	return askTurnWith(t, approver, hooks)
}

// askTurnWith is askTurn with the permission_ask hooks given whole.
func askTurnWith(t *testing.T, approver engine.Approver, ask []wire.Hook) []agent.ChatEvent {
	t.Helper()
	dir := t.TempDir()
	hooks := wire.UnifiedHooks{PermissionAsk: ask}
	raw, err := json.Marshal(hooks)
	require.NoError(t, err)
	hooksFile := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksFile, raw, 0o600))

	inst, err := mock.New().Instance(engine.Session{
		Mode: engine.Structured, WorkDir: dir,
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

// noHookDecided is the mock's deny when no hook answered.
const noHookDecided = "mock: no hook decided; Bash is denied"

// TestMockAsk_TheHooksAnswerDecides: an allow runs the call, a deny is the
// turn's denial.
func TestMockAsk_TheHooksAnswerDecides(t *testing.T) {
	use, result, denied, meta := callOutcome(t, askTurn(t, engine.ApproverHuman, `cat >/dev/null; printf '{"allow":true}'`))
	assert.Equal(t, "Bash", use.ToolName)
	assert.JSONEq(t, `{"command":"ls"}`, string(use.ToolInput))
	assert.False(t, result.IsError, "allowed: the call ran")
	assert.Nil(t, denied)
	assert.Empty(t, meta.Denials)

	_, result, denied, meta = callOutcome(t, askTurn(t, engine.ApproverHuman, `cat >/dev/null; printf '{"allow":false,"message":"not today"}'`))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Equal(t, "not today", denied.Reason)
	assert.Equal(t, []agent.PermissionDenial{*denied}, meta.Denials)
}

// TestMockAsk_WithNoHookAnswerTheCallIsDenied: a hook that writes nothing
// decides nothing, and nobody sits at the engine — the call is denied. The
// hook's payload names no call, as claude's permission ask names none.
func TestMockAsk_WithNoHookAnswerTheCallIsDenied(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "payload.json")
	_, result, denied, _ := callOutcome(t, askTurn(t, engine.ApproverHuman, `cat > `+payload))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Equal(t, noHookDecided, denied.Reason)
	body, err := os.ReadFile(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool":"Bash","input":{"command":"ls"}}`, string(body))
}

// TestMockAsk_NobodyToAskIsADenial: with any approver but the human the
// call is denied at once — no hook is consulted.
func TestMockAsk_NobodyToAskIsADenial(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	_, result, denied, meta := callOutcome(t, askTurn(t, engine.ApproverNone, `touch `+marker+`; printf '{"allow":true}'`))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Len(t, meta.Denials, 1)
	assert.NoFileExists(t, marker, "nobody to ask: no hook runs")
}

// TestMockAsk_AHookWhoseMatcherExcludesTheToolDecidesNothing: a hook is
// consulted only for the tools its matcher admits; one matched to another
// tool decides nothing, and the call is denied.
func TestMockAsk_AHookWhoseMatcherExcludesTheToolDecidesNothing(t *testing.T) {
	_, _, denied, _ := callOutcome(t, askTurnWith(t, engine.ApproverHuman, []wire.Hook{
		{Type: "command", Matcher: "Write", Command: `cat >/dev/null; printf '{"allow":true}'`},
	}))
	require.NotNil(t, denied, "the Write hook does not decide a Bash call")
	assert.Equal(t, noHookDecided, denied.Reason)
}

// TestMockApprovalCodec_HooksAreOnePermissionAskForEveryTool: the mock's
// approval route is one permission_ask hook (the mock's native events are
// the unified ones) admitting every tool, running ctxloom's hook for that
// event and outliving the approval timeout.
func TestMockApprovalCodec_HooksAreOnePermissionAskForEveryTool(t *testing.T) {
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	h := codec.Hooks(time.Minute)
	assert.Equal(t, []wire.Hook{agent.ApprovalHook(wire.HookEventPermissionAsk, "", time.Minute)}, h.PermissionAsk)
	assert.Len(t, h.All(), 1, "no other hook is the approval route's")
}

// TestMockAsk_AnExecFormHookRunsWithNoShell: an exec-form hook is spawned
// directly with its argv — nothing in an argument is expanded — as claude
// runs one.
func TestMockAsk_AnExecFormHookRunsWithNoShell(t *testing.T) {
	_, result, denied, _ := callOutcome(t, askTurnWith(t, engine.ApproverHuman, []wire.Hook{
		{Type: "command", Command: "printf", Args: []string{`{"allow":false,"message":"$HOME stays literal"}`}},
	}))
	assert.True(t, result.IsError)
	require.NotNil(t, denied)
	assert.Equal(t, "$HOME stays literal", denied.Reason)
}
