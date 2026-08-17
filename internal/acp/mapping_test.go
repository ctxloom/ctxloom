package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// testDecodeSessionUpdate parses a session/update notification's params,
// decoding the (SDK-typed-as-interface{}) `update` field into the typed
// SessionUpdate union. It exists only for these tests: production
// (HandleNotification, session.go) inlines this same two-step
// rawSessionUpdate+json.Unmarshal itself, with its own meta-update check
// spliced between the steps, rather than calling a shared helper —
// decodeSessionUpdate had no production caller and was deleted; this pins the
// same decode shape for the tests that need it).
func testDecodeSessionUpdate(t *testing.T, params json.RawMessage) (*api.SessionUpdate, error) {
	t.Helper()
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

// mapUpdateJSON wraps a raw `update` object in a session/update notification and
// runs it through the real decode+map path — the same one the live read loop
// uses — so these tests exercise wire decoding, not just in-memory structs.
func mapUpdateJSON(t *testing.T, updateJSON string) []agent.ChatEvent {
	t.Helper()
	params := []byte(`{"sessionId":"s1","update":` + updateJSON + `}`)
	upd, err := testDecodeSessionUpdate(t, params)
	require.NoError(t, err)
	return mapSessionUpdate(upd)
}

// oneEntry asserts the events are exactly one Entry event and returns it.
func oneEntry(t *testing.T, evs []agent.ChatEvent) *agent.SessionEntry {
	t.Helper()
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	return evs[0].Entry
}

// TestMapSessionUpdate_TextAndThinking pins the headline mapping: assistant text
// and — the win — summarized reasoning as EntryTypeThinking.
func TestMapSessionUpdate_TextAndThinking(t *testing.T) {
	t.Run("agent_message_chunk → assistant", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello world"}}`))
		assert.Equal(t, agent.EntryTypeAssistant, e.Type)
		assert.Equal(t, "hello world", e.Content)
	})

	t.Run("agent_thought_chunk → thinking", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"let me reason"}}`))
		assert.Equal(t, agent.EntryTypeThinking, e.Type)
		assert.Equal(t, "let me reason", e.Content)
	})

	t.Run("empty text chunk is dropped", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}`))
	})

	t.Run("non-text content block is dropped", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"aGk=","mimeType":"image/png"}}`))
	})
}

// TestMapSessionUpdate_ToolCall pins tool_call → tool_use (title→name, rawInput→input).
func TestMapSessionUpdate_ToolCall(t *testing.T) {
	t.Run("title + rawInput", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call","toolCallId":"tc1","title":"Read file","kind":"read","status":"pending","rawInput":{"path":"/etc/hosts"}}`))
		assert.Equal(t, agent.EntryTypeToolUse, e.Type)
		assert.Equal(t, "Read file", e.ToolName)
		assert.JSONEq(t, `{"path":"/etc/hosts"}`, string(e.ToolInput))
	})

	t.Run("falls back to kind when no title", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call","toolCallId":"tc2","kind":"execute","status":"pending"}`))
		assert.Equal(t, agent.EntryTypeToolUse, e.Type)
		assert.Equal(t, "execute", e.ToolName)
	})
}

// TestMapSessionUpdate_ToolCallUpdate pins tool_call_update → tool_result, only
// once it carries output or a terminal status; failure marks IsError.
func TestMapSessionUpdate_ToolCallUpdate(t *testing.T) {
	t.Run("completed with content → tool_result", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"file body"}}]}`))
		assert.Equal(t, agent.EntryTypeToolResult, e.Type)
		assert.Equal(t, "file body", e.ToolOutput)
		assert.False(t, e.IsError)
	})

	t.Run("failed with rawOutput → error tool_result", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"failed","rawOutput":"boom"}`))
		assert.Equal(t, agent.EntryTypeToolResult, e.Type)
		assert.Equal(t, "boom", e.ToolOutput)
		assert.True(t, e.IsError)
	})

	t.Run("diff content is flattened", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed","content":[{"type":"diff","path":"/a.txt","newText":"new"}]}`))
		assert.Contains(t, e.ToolOutput, "/a.txt")
		assert.Contains(t, e.ToolOutput, "new")
	})

	t.Run("bare in-progress tick yields nothing", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"in_progress"}`))
	})
}

// TestMapSessionUpdate_Plan pins plan → a system checklist entry, AND
// the structured entries surviving into SessionEntry.Plan with the
// SystemKindPlan discriminator — the fix for the conformance audit's
// headline finding: a prior revision only kept the flattened text, which the
// outbound side had no way to turn back into a real ACP `plan` update.
func TestMapSessionUpdate_Plan(t *testing.T) {
	e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"plan","entries":[{"content":"Do X","priority":"high","status":"pending"},{"content":"Do Y","priority":"medium","status":"in_progress"}]}`))
	assert.Equal(t, agent.EntryTypeSystem, e.Type)
	assert.Contains(t, e.Content, "[pending] Do X")
	assert.Contains(t, e.Content, "[in_progress] Do Y")

	assert.Equal(t, agent.SystemKindPlan, e.SystemKind)
	require.Len(t, e.Plan, 2)
	assert.Equal(t, agent.PlanEntry{Content: "Do X", Priority: "high", Status: "pending"}, e.Plan[0])
	assert.Equal(t, agent.PlanEntry{Content: "Do Y", Priority: "medium", Status: "in_progress"}, e.Plan[1])
}

// TestMapSessionUpdate_ToolCallIDPreserved pins the tool-call id fix: the
// engine's own toolCallId (and kind/locations) survive into the IR instead of
// being dropped at the flatten boundary — this is what lets the outbound
// re-emission reuse the SAME id instead of generating one keyed only by tool
// name (the confirmed same-name mispair risk).
func TestMapSessionUpdate_ToolCallIDPreserved(t *testing.T) {
	e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call","toolCallId":"engine-call-7","title":"Read file","kind":"read","status":"pending","locations":[{"path":"/a.txt","line":3}]}`))
	assert.Equal(t, "engine-call-7", e.ToolCallID)
	assert.Equal(t, "read", e.ToolKind)
	require.Len(t, e.ToolLocations, 1)
	assert.Equal(t, agent.ToolLocation{Path: "/a.txt", Line: 3}, e.ToolLocations[0])

	r := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"engine-call-7","status":"completed","content":[{"type":"content","content":{"type":"text","text":"body"}}]}`))
	assert.Equal(t, "engine-call-7", r.ToolCallID)
	require.Len(t, r.ToolContent, 1)
	assert.Equal(t, "content", r.ToolContent[0].Kind)
	assert.Equal(t, "body", r.ToolContent[0].Text)
}

// TestMapSessionUpdate_RawPassthrough pins the invariant that
// available_commands_update and current_mode_update have no IR entry type of
// their own, but are forwarded on the ChatEvent.Raw side channel instead of
// silently dropped — and a `_meta` supplement on an otherwise
// fully-mapped entry rides alongside it.
func TestMapSessionUpdate_RawPassthrough(t *testing.T) {
	t.Run("available_commands_update forwarded as a raw-only event", func(t *testing.T) {
		evs := mapUpdateJSON(t, `{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"foo","description":"d"}]}`)
		require.Len(t, evs, 1)
		assert.Nil(t, evs[0].Entry)
		require.NotEmpty(t, evs[0].Raw)
		assert.Contains(t, string(evs[0].Raw), `"sessionUpdate":"available_commands_update"`)
		assert.Contains(t, string(evs[0].Raw), `"foo"`)
	})

	t.Run("current_mode_update forwarded as a raw-only event", func(t *testing.T) {
		evs := mapUpdateJSON(t, `{"sessionUpdate":"current_mode_update","currentModeId":"review"}`)
		require.Len(t, evs, 1)
		assert.Nil(t, evs[0].Entry)
		require.NotEmpty(t, evs[0].Raw)
		assert.Contains(t, string(evs[0].Raw), `"currentModeId":"review"`)
	})

	t.Run("_meta on a mapped entry rides alongside it, not instead of it", func(t *testing.T) {
		evs := mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"},"_meta":{"vendor":{"x":1}}}`)
		require.Len(t, evs, 1)
		require.NotNil(t, evs[0].Entry)
		assert.Equal(t, "hi", evs[0].Entry.Content)
		require.NotEmpty(t, evs[0].Raw)
		assert.JSONEq(t, `{"_meta":{"vendor":{"x":1}}}`, string(evs[0].Raw))
	})

	t.Run("no _meta means no Raw", func(t *testing.T) {
		evs := mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)
		require.Len(t, evs, 1)
		assert.Empty(t, evs[0].Raw)
	})
}

// TestMapSessionUpdate_Dropped pins the variants and shapes that must yield no
// entries (never crash, never duplicate the user's own message).
func TestMapSessionUpdate_Dropped(t *testing.T) {
	t.Run("user_message_chunk is not echoed back", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"my prompt"}}`))
	})

	t.Run("nil update", func(t *testing.T) {
		assert.Empty(t, mapSessionUpdate(nil))
	})

	t.Run("unknown sessionUpdate variant never crashes the stream", func(t *testing.T) {
		// RESTORED: the hand-rolled union the acp-go-sdk fork replaced rejected any unmodeled
		// discriminator outright — a decode error the live handler turned
		// into a logged warning. The fork's GENERATED SessionUpdate.
		// UnmarshalJSON regressed that: once its discriminator switch and
		// per-variant required-field checks both missed, it fell back to a
		// bare json.Unmarshal into EVERY variant type in turn and accepted
		// the first one that didn't error — silently decoding a body like
		// this one into UserMessageChunk{} instead of reporting it as the
		// unrecognized frame it actually was. The generator now rejects an
		// unrecognized discriminator outright for unions (like
		// SessionUpdate) where every variant carries its own const value,
		// so decodeSessionUpdate correctly errors here again.
		//
		// What this test still guards, unchanged: the STREAM must never
		// crash and no entry must ever be emitted for an unrecognized
		// frame. That now happens the way it always should have — a real
		// decode error, which HandleNotification's existing "dropping
		// malformed session/update" warn-and-continue path (session.go)
		// already handles — not by silently mis-decoding into a real (but
		// wrong) variant that happens to map to nothing.
		upd, err := testDecodeSessionUpdate(t, []byte(`{"sessionId":"s","update":{"sessionUpdate":"quantum_flux"}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrecognized")
		assert.Nil(t, upd)
		// mapSessionUpdate itself is nil-safe (see the "nil update" case
		// above) — HandleNotification's caller-side drop never reaches it
		// for a decode failure, but the nil-safety is what makes the drop
		// path harmless either way.
		assert.Empty(t, mapSessionUpdate(upd))
	})
}

// TestDecidePermission pins the allow/deny decision (mirrors the claude driver:
// allow only under a bypass posture) and the option-kind preference.
func TestDecidePermission(t *testing.T) {
	full := []api.PermissionOption{
		{Kind: api.PermissionOptionKindAllowAlways, Name: "Always allow", OptionId: "aa"},
		{Kind: api.PermissionOptionKindAllowOnce, Name: "Allow once", OptionId: "ao"},
		{Kind: api.PermissionOptionKindRejectOnce, Name: "Reject once", OptionId: "ro"},
		{Kind: api.PermissionOptionKindRejectAlways, Name: "Reject always", OptionId: "ra"},
	}

	t.Run("allow selects allow_once (preferred over allow_always)", func(t *testing.T) {
		got := decidePermission(full, true)
		assert.Equal(t, outcomeSelected, got.Outcome.Outcome)
		assert.Equal(t, "ao", got.Outcome.OptionId)
	})

	t.Run("deny selects reject_once", func(t *testing.T) {
		got := decidePermission(full, false)
		assert.Equal(t, outcomeSelected, got.Outcome.Outcome)
		assert.Equal(t, "ro", got.Outcome.OptionId)
	})

	t.Run("deny with no reject option falls back to cancelled", func(t *testing.T) {
		got := decidePermission([]api.PermissionOption{{Kind: api.PermissionOptionKindAllowOnce, OptionId: "ao"}}, false)
		assert.Equal(t, outcomeCancelled, got.Outcome.Outcome)
		assert.Empty(t, got.Outcome.OptionId)
	})

	t.Run("allow with no options falls back to cancelled", func(t *testing.T) {
		got := decidePermission(nil, true)
		assert.Equal(t, outcomeCancelled, got.Outcome.Outcome)
	})
}

// TestMapToolCallUpdate_TerminalOnlyContentIsReported pins a tool_call_update
// whose content is a TERMINAL reference and nothing else. Terminal content
// carries no text of its own (its output is fetched over terminal/output, see
// toolContentText), so the flattened ToolOutput is legitimately empty — but the
// update is NOT status noise: it is how the engine tells the client WHERE the
// tool's live output is going, and dropping it leaves a frontend unable to
// attach to the terminal at all. The structural terminal id must reach the IR.
func TestMapToolCallUpdate_TerminalOnlyContentIsReported(t *testing.T) {
	events := mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","content":[{"type":"terminal","terminalId":"term-7"}]}`)
	e := oneEntry(t, events)

	assert.Equal(t, agent.EntryTypeToolResult, e.Type)
	require.Len(t, e.ToolContent, 1, "the terminal reference must survive structurally")
	assert.Equal(t, "terminal", e.ToolContent[0].Kind)
	assert.Equal(t, "term-7", e.ToolContent[0].TerminalID)
	assert.Empty(t, e.ToolOutput, "terminal content has no text to flatten — its output rides terminal/output")
}

// TestRawOnlyEvent_UnmarshalableUpdateIsDroppedLoudly pins the diagnostic on the
// raw-passthrough envelope's drop path. It is reachable: a union with no variant
// set makes api.SessionUpdate.MarshalJSON hand encoding/json zero bytes, which
// encoding/json rejects — and the frame then vanishes, where every analogous
// decode failure in session.go warns. A dropped frame nobody reports is a
// frontend missing an update with no evidence anywhere that one existed.
func TestRawOnlyEvent_UnmarshalableUpdateIsDroppedLoudly(t *testing.T) {
	_, err := json.Marshal(&api.SessionUpdate{})
	require.Error(t, err, "a variant-less union cannot be marshaled — this is the drop path")

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	assert.Empty(t, rawOnlyEvent(&api.SessionUpdate{}), "an empty Raw must never be emitted as an event")
	assert.Contains(t, warnings.String(), "acp: dropping", "the drop must be diagnosable")
}
