package acpagent

import (
	"encoding/json"
	"io"
	"testing"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestMapEvent_SystemEntryEmitsVisibleFrame covers the Q1 bug: an ACP `plan`
// update lands in the IR as EntryTypeSystem (internal/acp/mapping.go's
// mapPlan renders the plan entries into a single Content string — no
// structured plan survives), and outbound mapping used to hit the
// `default: nil` case and drop it entirely. The editor would receive zero
// bytes for a plan the engine actually sent. This asserts the actual emitted
// frame's shape/bytes, not merely that mapEvent returned without error — a
// no-op mapEvent would still "succeed" by that weaker bar.
func TestMapEvent_SystemEntryEmitsVisibleFrame(t *testing.T) {
	sess := &session{}
	planText := "Plan:\n- [pending] Read the file\n- [in_progress] Apply the fix"
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeSystem,
		Content: planText,
	}}

	updates := sess.mapEvent(ev)

	require.Len(t, updates, 1, "a non-empty system entry must produce exactly one outbound frame")
	u := updates[0]
	require.NotNil(t, u.AgentMessageChunk, "agent_message_chunk payload must be present")
	require.NotNil(t, u.AgentMessageChunk.Content.Text)
	require.Equal(t, planText, u.AgentMessageChunk.Content.Text.Text,
		"the plan's rendered text must survive byte-for-byte into the outbound chunk")
}

// TestMapEvent_SystemEntryEmptyContentDropped guards the empty-content edge:
// an empty system entry (there is currently no producer of one, but the
// contract should stay honest) still yields no frame rather than an empty
// visible chunk.
func TestMapEvent_SystemEntryEmptyContentDropped(t *testing.T) {
	sess := &session{}
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeSystem, Content: ""}}

	updates := sess.mapEvent(ev)

	require.Nil(t, updates)
}

// TestMapEvent_PlanEntryEmitsRealPlanFrame pins the headline fix: a system
// entry carrying SystemKind==SystemKindPlan and structured Plan entries now
// re-emits a REAL ACP `plan` update with the structured PlanEntry list
// intact — not the flattened text fallback (see
// TestMapEvent_SystemEntryEmitsVisibleFrame above, which still covers the
// OTHER EntryTypeSystem producer: the delegated-turn-failure notice, whose
// SystemKind is the zero value and therefore takes that same fallback path
// unchanged).
func TestMapEvent_PlanEntryEmitsRealPlanFrame(t *testing.T) {
	sess := &session{}
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeSystem,
		Content:    "Plan:\n- [pending] Read the file\n- [in_progress] Apply the fix",
		SystemKind: agent.SystemKindPlan,
		Plan: []agent.PlanEntry{
			{Content: "Read the file", Priority: "high", Status: "pending"},
			{Content: "Apply the fix", Priority: "medium", Status: "in_progress"},
		},
	}}

	updates := sess.mapEvent(ev)

	require.Len(t, updates, 1, "a plan entry must produce exactly one outbound frame")
	u := updates[0]
	require.NotNil(t, u.Plan, "a real ACP `plan` update must be emitted, not agent_message_chunk")
	require.Len(t, u.Plan.Entries, 2)
	assert.Equal(t, "Read the file", u.Plan.Entries[0].Content)
	assert.EqualValues(t, "high", u.Plan.Entries[0].Priority)
	assert.EqualValues(t, "pending", u.Plan.Entries[0].Status)
	assert.Equal(t, "Apply the fix", u.Plan.Entries[1].Content)
	assert.EqualValues(t, "in_progress", u.Plan.Entries[1].Status)

	// The wire bytes: a real spec `plan` update, structured entries intact.
	raw, err := json.Marshal(u)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"sessionUpdate":"plan"`)
	assert.Contains(t, string(raw), `"content":"Read the file"`)
	assert.Contains(t, string(raw), `"status":"pending"`)
	assert.NotContains(t, string(raw), "agentMessageChunk")
}

// TestMapEvent_ToolCallIDPreserved is the same-name-mispair fix: when the
// producing backend supplied an engine-native ToolCallID (e.g.
// internal/acp's ACP-native client mapping), the outbound tool_call and its
// tool_call_update both carry that SAME id — not a fresh "call-N" the old
// FIFO-by-name scheme would have generated, which could pair a result with
// the WRONG concurrent call to the same tool.
func TestMapEvent_ToolCallIDPreserved(t *testing.T) {
	sess := &session{openCall: make(map[string][]api.ToolCallId)}

	useUpdates := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolUse, ToolName: "fs_read", ToolCallID: "engine-call-42",
	}})
	require.Len(t, useUpdates, 1)
	require.NotNil(t, useUpdates[0].ToolCall)
	assert.Equal(t, api.ToolCallId("engine-call-42"), useUpdates[0].ToolCall.ToolCallId)

	// A SECOND concurrent call to the SAME tool name, a different engine id —
	// this is exactly the scenario the FIFO-by-name scheme could mispair.
	useUpdates2 := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolUse, ToolName: "fs_read", ToolCallID: "engine-call-43",
	}})
	require.Len(t, useUpdates2, 1)
	assert.Equal(t, api.ToolCallId("engine-call-43"), useUpdates2[0].ToolCall.ToolCallId)

	// The SECOND call's result arrives FIRST (out of push order) — a FIFO
	// pop-by-name would have wrongly paired this with engine-call-42.
	resultUpdates := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolCallID: "engine-call-43", ToolOutput: "second",
	}})
	require.Len(t, resultUpdates, 1)
	require.NotNil(t, resultUpdates[0].ToolCallUpdate)
	assert.Equal(t, api.ToolCallId("engine-call-43"), resultUpdates[0].ToolCallUpdate.ToolCallId)

	resultUpdates1 := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolCallID: "engine-call-42", ToolOutput: "first",
	}})
	require.Len(t, resultUpdates1, 1)
	assert.Equal(t, api.ToolCallId("engine-call-42"), resultUpdates1[0].ToolCallUpdate.ToolCallId)
}

// TestMapEvent_ToolCallIDFallback pins backward compatibility: a backend that
// supplies NO ToolCallID (every non-ACP-native producer today) still gets
// the original generated-id/FIFO-pairing behavior byte-for-byte.
func TestMapEvent_ToolCallIDFallback(t *testing.T) {
	sess := &session{openCall: make(map[string][]api.ToolCallId)}

	useUpdates := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "fs_read"}})
	require.Len(t, useUpdates, 1)
	genID := useUpdates[0].ToolCall.ToolCallId
	assert.NotEmpty(t, genID)

	resultUpdates := sess.mapEvent(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolOutput: "ok"}})
	require.Len(t, resultUpdates, 1)
	assert.Equal(t, genID, resultUpdates[0].ToolCallUpdate.ToolCallId)
}

// TestMapEvent_RawOnlyPassthrough pins the outbound half: a raw-only
// ChatEvent (Entry nil, Raw set — internal/acp/mapping.go's
// available_commands_update/current_mode_update forwarding) re-decodes and
// re-emits, while a non-allowlisted or malformed Raw payload is dropped —
// the allowlist is enforced again at THIS boundary, not just trusted from
// upstream.
func TestMapEvent_RawOnlyPassthrough(t *testing.T) {
	sess := &session{}

	t.Run("allowlisted variant re-emitted", func(t *testing.T) {
		raw := json.RawMessage(`{"sessionUpdate":"current_mode_update","currentModeId":"review"}`)
		updates := sess.mapEvent(agent.ChatEvent{Raw: raw})
		require.Len(t, updates, 1)
		require.NotNil(t, updates[0].CurrentModeUpdate)
		assert.EqualValues(t, "review", updates[0].CurrentModeUpdate.CurrentModeId)
	})

	t.Run("nil Raw yields nothing", func(t *testing.T) {
		assert.Nil(t, sess.mapEvent(agent.ChatEvent{}))
	})

	t.Run("malformed Raw is dropped, not forwarded blind", func(t *testing.T) {
		assert.Nil(t, sess.mapEvent(agent.ChatEvent{Raw: json.RawMessage(`not json`)}))
	})
}

// TestContentBlocksFromACP_MarshalFailureIsLoud pins that when
// json.Marshal of a prompt block failed, contentBlocksFromACP `continue`d,
// dropping that block from the structured payload entirely — the exact silent
// drop the function exists to prevent, and with nothing said anywhere.
//
// Reaching the branch needs a block whose _meta carries a value encoding/json
// refuses (a func); a block decoded off the wire can never hold one, so this
// is defense in depth rather than a live wire path. The invariant being pinned
// is nonetheless the one that matters: this function never loses a block in
// silence.
func TestContentBlocksFromACP_MarshalFailureIsLoud(t *testing.T) {
	var sink syncWriter
	restore := clidiag.SetSink(&sink)
	defer restore()

	unmarshalable := api.ContentBlock{Text: &api.ContentBlockText{
		Text: "carried",
		Meta: map[string]any{"nope": func() {}},
	}}
	require.Error(t, json.NewEncoder(io.Discard).Encode(unmarshalable),
		"the fixture must really be unmarshalable, or this test proves nothing")

	out := contentBlocksFromACP([]api.ContentBlock{unmarshalable})

	assert.Empty(t, out, "an unmarshalable block still cannot be carried")
	assert.Contains(t, sink.String(), "session/prompt",
		"dropping a prompt block must be reported, never silent")
}

// TestUsageUpdateWire_CostOnlyTurnReportsNoGauge pins that a turn that
// reports only cost (no InputTokens, no ContextWindow) passes
// usageUpdateWire's guard and used to emit {"used":0,"size":0,"cost":{...}}.
// api.SessionUsageUpdate declares `used` and `size` without omitempty, so
// both were always on the wire — and an editor rendering the context gauge
// from them reads a real, specific claim: this session has consumed 0 tokens
// of a 0-token window. That is a fabricated measurement, not a missing one.
//
// schema-v1.19.0's UsageUpdate has no `required` array at all, so both fields
// are optional and omitting an unknown one is the spec-sanctioned way to say
// "no gauge". A generated Go consumer decodes an absent field to 0 exactly as
// it decoded an explicit 0, so nothing that parses this frame today changes
// its mind about anything.
func TestUsageUpdateWire_CostOnlyTurnReportsNoGauge(t *testing.T) {
	raw, err := json.Marshal(usageUpdateWire(&agent.TurnMeta{CostUSD: 0.02}))
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"cost"`, "the cost this turn DID report must still be delivered")
	assert.NotContains(t, string(raw), `"used"`, "no token count was reported — claiming 0 invents one")
	assert.NotContains(t, string(raw), `"size"`, "no context window was reported — claiming 0 invents one")
}

// TestUsageUpdateWire_RealGaugeStillRidesTheWire is the other half: a turn
// that DOES report accounting must still emit both numbers, so the fix above
// cannot be satisfied by dropping the gauge altogether.
func TestUsageUpdateWire_RealGaugeStillRidesTheWire(t *testing.T) {
	raw, err := json.Marshal(usageUpdateWire(&agent.TurnMeta{InputTokens: 1234, ContextWindow: 100000, CostUSD: 0.02}))
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"used":1234`)
	assert.Contains(t, string(raw), `"size":100000`)
	assert.Contains(t, string(raw), `"cost"`)
}
