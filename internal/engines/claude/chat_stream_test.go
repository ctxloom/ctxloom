package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture reads testdata/streamjson/<name> from THIS FILE's own location
// rather than the working directory: TestMain moves the whole binary into a
// throwaway sandbox cwd, so a relative "testdata/..." resolves to nothing
// there — and fails as a missing FILE, which reads like a broken fixture.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(dir, "testdata", "streamjson", name))
	require.NoError(t, err)
	return b
}

func TestMapStreamJSONEvent_AssistantText_OneAssistantEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "assistant_text.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
	assert.Equal(t, "hello-from-tool", evs[0].Entry.Content)
}

func TestMapStreamJSONEvent_AssistantToolUse_OneToolUseEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "assistant_tooluse.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolUse, evs[0].Entry.Type)
	assert.Equal(t, "Bash", evs[0].Entry.ToolName)
	assert.Contains(t, string(evs[0].Entry.ToolInput), "echo hello-from-tool")
	// The call's id is the approval route's correlation key (the runner's
	// ledger): a tool_use without it can never anchor a permission request.
	assert.Equal(t, "toolu_01VuNv2eXbKS3shVDAQ1zMBX", evs[0].Entry.ToolCallID)
}

func TestMapStreamJSONEvent_ToolResult_OneToolResultEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "user_toolresult.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolResult, evs[0].Entry.Type)
	assert.Equal(t, "hello-from-tool", evs[0].Entry.ToolOutput)
	assert.False(t, evs[0].Entry.IsError)
	// The result names the call it answers: what releases a held host.
	assert.Equal(t, "toolu_01VuNv2eXbKS3shVDAQ1zMBX", evs[0].Entry.ToolCallID)
}

func TestMapStreamJSONEvent_Result_CompleteWithMetadata(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "result_success.json"))
	require.Len(t, evs, 1)
	c := evs[0].Complete
	require.NotNil(t, c)
	assert.Equal(t, "claude-opus-4-8", c.Model)
	assert.Equal(t, 1_000_000, c.ContextWindow)
	assert.Equal(t, 64_000, c.MaxOutputTokens)
	assert.Greater(t, c.InputTokens, 0)
	assert.Greater(t, c.CacheReadTokens, 0)
	assert.InDelta(t, 0.1667485, c.CostUSD, 1e-6)
	assert.Equal(t, "end_turn", c.StopReason)
	assert.Equal(t, 1, c.NumTurns)
	assert.Nil(t, evs[0].Entry, "result is metadata only — no content (no double-render of result string)")
}

func TestMapStreamJSONEvent_SystemInit_SessionInfo(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "system_init.json"))
	require.Len(t, evs, 1)
	s := evs[0].Session
	require.NotNil(t, s)
	assert.Equal(t, "claude-sonnet-4-6", s.Model)
	assert.Equal(t, "bypassPermissions", s.PermissionMode)
	require.NotEmpty(t, s.MCPServers)
	assert.Equal(t, "spotify", s.MCPServers[0].Name)
	assert.Equal(t, "connected", s.MCPServers[0].Status)
}

func TestMapStreamJSONEvent_RateLimit_Dropped(t *testing.T) {
	assert.Empty(t, mapStreamJSONEvent(fixture(t, "rate_limit_event.json")))
}

// A new/unknown event type, malformed JSON, and the many dropped system subtypes
// must all yield nothing rather than crash the stream.
func TestMapStreamJSONEvent_UnknownOrNoise_Dropped(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"type":"totally_new_event_type"}`),
		[]byte(`{"type":"system","subtype":"hook_started"}`),
		[]byte(`{"type":"system","subtype":"thinking_tokens"}`),
		[]byte(`{"type":"system","subtype":"commands_changed"}`),
		[]byte(`not even json`),
		[]byte(``),
	} {
		assert.Empty(t, mapStreamJSONEvent(raw), "should drop: %s", raw)
	}
}

// One assistant event with multiple content blocks expands to one Entry per
// renderable block, in order; thinking blocks are dropped.
func TestMapStreamJSONEvent_MultiBlockAssistant_ExpandsInOrder(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[
		{"type":"thinking","thinking":"hmm"},
		{"type":"tool_use","name":"Read","input":{"path":"x"}},
		{"type":"text","text":"done"}]}}`)
	evs := mapStreamJSONEvent(raw)
	require.Len(t, evs, 3)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeThinking, evs[0].Entry.Type)
	assert.Equal(t, "hmm", evs[0].Entry.Content)
	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, agent.EntryTypeToolUse, evs[1].Entry.Type)
	assert.Equal(t, "Read", evs[1].Entry.ToolName)
	require.NotNil(t, evs[2].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[2].Entry.Type)
	assert.Equal(t, "done", evs[2].Entry.Content)
}

func TestMapStreamJSONEvent_Thinking_OneThinkingEntry(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me reason","signature":"sig"}]}}`)
	evs := mapStreamJSONEvent(raw)
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeThinking, evs[0].Entry.Type)
	assert.Equal(t, "let me reason", evs[0].Entry.Content)
}

func TestMapStreamJSONEvent_EmptyThinking_EmittedAsMarker(t *testing.T) {
	// claude-code's -p stream-json redacts thinking text to "" (signature only).
	// The live stream still emits a content-less thinking marker so a frontend can
	// show the model reasoned this turn.
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`)
	evs := mapStreamJSONEvent(raw)
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeThinking, evs[0].Entry.Type)
	assert.Equal(t, "", evs[0].Entry.Content)
}

// TestMapStreamJSON_PermissionDenied: claude's system/permission_denied frame
// (the live 2.1.283 shape: tool_name, tool_use_id, message) is a denial the
// engine decided — it surfaces as ChatEvent.Denied, never dropped.
func TestMapStreamJSON_PermissionDenied(t *testing.T) {
	evs := mapStreamJSONEvent([]byte(`{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"toolu_01Qa","message":"Output redirection to 'b.txt' needs approval."}`))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Denied)
	assert.Equal(t, agent.PermissionDenial{ToolName: "Bash", ToolCallID: "toolu_01Qa", Reason: "Output redirection to 'b.txt' needs approval.", Decider: agent.DeciderPolicy}, *evs[0].Denied)
	assert.Equal(t, "denied", evs[0].Kind())
}

// TestMapStreamJSON_ResultPermissionDenials: result.permission_denials (the
// live shape: tool_name, tool_use_id, tool_input — no reason) rides the
// completion as TurnMeta.Denials; an empty list is no denials.
func TestMapStreamJSON_ResultPermissionDenials(t *testing.T) {
	evs := mapStreamJSONEvent([]byte(`{"type":"result","subtype":"success","stop_reason":"end_turn","permission_denials":[{"tool_name":"Write","tool_use_id":"toolu_012f","tool_input":{"file_path":"w.txt","content":"red"}},{"tool_name":"Bash","tool_use_id":"toolu_01Fn","tool_input":{"command":"echo red > w.txt"}}]}`))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Complete)
	assert.Equal(t, []agent.PermissionDenial{
		{ToolName: "Write", ToolCallID: "toolu_012f", Decider: agent.DeciderPolicy},
		{ToolName: "Bash", ToolCallID: "toolu_01Fn", Decider: agent.DeciderPolicy},
	}, evs[0].Complete.Denials)

	evs = mapStreamJSONEvent([]byte(`{"type":"result","subtype":"success","permission_denials":[]}`))
	require.Len(t, evs, 1)
	assert.Empty(t, evs[0].Complete.Denials)
}

// THE AUTH-FAILURE SHAPE. assistant_auth_failed.json is NOT a capture: no
// credential was available to provoke one, and a real one is never
// invalidated to get it. It is built from claude's DOCUMENTED type —
// SDKAssistantMessage in the Agent SDK TypeScript reference
// (code.claude.com/docs/en/agent-sdk/typescript): `error?:
// SDKAssistantMessageError` on a `type:"assistant"` message beside
// `parent_tool_use_id: string | null`. claude's own SDK host reads an auth
// failure from exactly that: a top-level (parent_tool_use_id null) assistant
// message whose error is authentication_failed or oauth_org_not_allowed. The
// unrun @live cell AUTH1 replaces it with a capture.

// authFrame is the fixture with its error and parent overridden.
func authFrame(t *testing.T, errValue string, parent any) []byte {
	t.Helper()
	var frame map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "assistant_auth_failed.json"), &frame))
	frame["error"] = errValue
	frame["parent_tool_use_id"] = parent
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

// failuresIn is every Failed event among evs.
func failuresIn(evs []agent.ChatEvent) []agent.TurnFailure {
	var out []agent.TurnFailure
	for _, ev := range evs {
		if ev.Failed != nil {
			out = append(out, *ev.Failed)
		}
	}
	return out
}

func TestMapStreamJSONEvent_AuthFailed_CredentialRejected(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "assistant_auth_failed.json"))
	require.Len(t, evs, 2, "claude's words stay an entry; the failure rides beside them")
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
	assert.Equal(t, []agent.TurnFailure{{Kind: agent.FailureCredentialRejected}}, failuresIn(evs))
}

func TestMapStreamJSONEvent_OAuthOrgNotAllowed_CredentialRejected(t *testing.T) {
	evs := mapStreamJSONEvent(authFrame(t, "oauth_org_not_allowed", nil))
	assert.Equal(t, []agent.TurnFailure{{Kind: agent.FailureCredentialRejected}}, failuresIn(evs))
}

// A sub-agent's message carries its parent's tool_use_id: its failure is the
// sub-agent's, which the turn's own engine reports as a tool result, not the
// turn's credential dying.
func TestMapStreamJSONEvent_SubagentAuthFailure_NotTheTurns(t *testing.T) {
	evs := mapStreamJSONEvent(authFrame(t, "authentication_failed", "toolu_01VuNv2eXbKS3shVDAQ1zMBX"))
	assert.Empty(t, failuresIn(evs))
	require.Len(t, evs, 1, "the sub-agent's words are still relayed")
}

// Only a refused CREDENTIAL parks a run. claude's other error values are not
// one: a rate limit waits (shared backoff is its own concern), an account on
// hold is not fixed by signing in again, and a value this build does not know
// is claude's "unknown".
func TestMapStreamJSONEvent_OtherAPIErrors_NotACredential(t *testing.T) {
	for _, v := range []string{"rate_limit", "overloaded", "account_on_hold", "cloud_credential_error", "billing_error", "unknown", "a_value_from_a_later_release"} {
		t.Run(v, func(t *testing.T) {
			assert.Empty(t, failuresIn(mapStreamJSONEvent(authFrame(t, v, nil))))
		})
	}
}
