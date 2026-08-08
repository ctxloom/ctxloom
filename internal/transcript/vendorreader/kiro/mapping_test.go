// Pure-function table tests for the mapping functions kiro_test.go's
// golden-fixture pipeline test does not exercise (a successful, non-empty
// ToolUseResults; locator parsing edge cases; EnumerateConversations) —
// mirrors codex/mapping_test.go's split: these need no Recorder and no
// sqlite db beyond what EnumerateConversations' own test builds by hand.
package kiro

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestToolResultEvents_SuccessWithText uses the ONE real, non-empty-Text
// ToolUseResults success case found across every conversation on this box
// (testdata/MANIFEST.json: conversation_id 82661ac2-..., a real fs_read
// call/result pair) — codex's mapping_test.go's identical discipline of
// covering a shape the main golden fixture doesn't (there, reasoning
// summaries; here, a SUCCESS status with non-empty output, since
// conversation-fixture.json's own ToolUseResults-shaped case is the
// CancelledToolUses Error path).
func TestToolResultEvents_SuccessWithText(t *testing.T) {
	results := []toolUseResult{
		{
			ToolUseID: "tooluse_EwOLTyvr0mQrXDUUOBlXUi",
			Content:   []toolResultContent{{Text: "hello world test file"}},
			Status:    "Success",
		},
	}
	evs := toolResultEvents(results)
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolResult, evs[0].Entry.Type)
	assert.Equal(t, "tooluse_EwOLTyvr0mQrXDUUOBlXUi", evs[0].Entry.ToolCallID)
	assert.Equal(t, "hello world test file", evs[0].Entry.ToolOutput)
	assert.False(t, evs[0].Entry.IsError, "status \"Success\" must never surface as an error")
}

func TestToolResultEvents_NonSuccessStatusIsError(t *testing.T) {
	for _, status := range []string{"Error", "Cancelled", "", "anything-else", "success", "SUCCESS", " Success"} {
		t.Run(status, func(t *testing.T) {
			evs := toolResultEvents([]toolUseResult{{ToolUseID: "x", Status: status}})
			require.Len(t, evs, 1)
			assert.True(t, evs[0].Entry.IsError, "any status other than the literal \"Success\" must surface as IsError")
		})
	}
}

func TestToolResultEvents_Empty(t *testing.T) {
	assert.Nil(t, toolResultEvents(nil))
}

// TestToolResultEvents_JoinsMultiplePartsDroppingEmpty pins the same
// empty-Text-must-not-contribute-a-blank-paragraph behavior the deleted
// joinToolResultText helper had its own direct test for, now exercised
// through the one production caller instead of a pass-through wrapper.
func TestToolResultEvents_JoinsMultiplePartsDroppingEmpty(t *testing.T) {
	evs := toolResultEvents([]toolUseResult{{
		ToolUseID: "tooluse_multi",
		Content:   []toolResultContent{{Text: "first"}, {Text: ""}, {Text: "second"}},
		Status:    "Success",
	}})
	require.Len(t, evs, 1)
	assert.Equal(t, "first\n\nsecond", evs[0].Entry.ToolOutput, "empty Text elements must not contribute a blank paragraph")
}

func TestUserContentEvents_UnrecognizedVariantSkipped(t *testing.T) {
	evs := userContentEvents(json.RawMessage(`{"SomeFutureVariant": {"whatever": true}}`))
	assert.Nil(t, evs, "an unmodeled userContent variant must skip, not error or panic")
}

func TestUserContentEvents_MalformedJSONSkipped(t *testing.T) {
	evs := userContentEvents(json.RawMessage(`not json at all`))
	assert.Nil(t, evs)
}

func TestUserContentEvents_EmptyPromptSkipped(t *testing.T) {
	evs := userContentEvents(json.RawMessage(`{"Prompt": {"prompt": ""}}`))
	assert.Nil(t, evs, "an empty prompt string contributes nothing")
}

// TestUserContentEvents_CancelledToolUsesOrder pins the deliberate ordering
// decision in userContentEvents: the rejection notice (system entry) before
// the tool_result entries it explains.
func TestUserContentEvents_CancelledToolUsesOrder(t *testing.T) {
	raw := json.RawMessage(`{"CancelledToolUses": {
		"prompt": "rejected: policy",
		"tool_use_results": [{"tool_use_id": "t1", "content": [{"Text": "cancelled"}], "status": "Error"}]
	}}`)
	evs := userContentEvents(raw)
	require.Len(t, evs, 2)
	assert.Equal(t, agent.EntryTypeSystem, evs[0].Entry.Type)
	assert.Equal(t, agent.SystemKindNotice, evs[0].Entry.SystemKind)
	assert.Equal(t, "rejected: policy", evs[0].Entry.Content)
	assert.Equal(t, agent.EntryTypeToolResult, evs[1].Entry.Type)
	assert.True(t, evs[1].Entry.IsError)
}

func TestUserContentEvents_CancelledToolUsesEmptyPromptSkipsNotice(t *testing.T) {
	raw := json.RawMessage(`{"CancelledToolUses": {
		"prompt": "",
		"tool_use_results": [{"tool_use_id": "t1", "content": [], "status": "Error"}]
	}}`)
	evs := userContentEvents(raw)
	require.Len(t, evs, 1, "an empty rejection prompt must not synthesize a blank system entry")
	assert.Equal(t, agent.EntryTypeToolResult, evs[0].Entry.Type)
}

// TestAssistantContentEvents_ToolUseRealShape uses the real fs_read tool
// call paired with TestToolResultEvents_SuccessWithText's result
// (testdata/MANIFEST.json).
func TestAssistantContentEvents_ToolUseRealShape(t *testing.T) {
	raw := json.RawMessage(`{"ToolUse": {
		"message_id": "e2954c63-e3f2-4a1b-9671-fb57e8380e5d",
		"content": "",
		"tool_uses": [{
			"id": "tooluse_EwOLTyvr0mQrXDUUOBlXUi",
			"name": "fs_read",
			"args": {"operations":[{"mode":"Line","path":"/tmp/probe.txt"}]}
		}]
	}}`)
	evs := assistantContentEvents(raw)
	require.Len(t, evs, 1, "empty ToolUse.content must not add a leading assistant entry")
	assert.Equal(t, agent.EntryTypeToolUse, evs[0].Entry.Type)
	assert.Equal(t, "fs_read", evs[0].Entry.ToolName)
	assert.Equal(t, "tooluse_EwOLTyvr0mQrXDUUOBlXUi", evs[0].Entry.ToolCallID)
	assert.JSONEq(t, `{"operations":[{"mode":"Line","path":"/tmp/probe.txt"}]}`, string(evs[0].Entry.ToolInput))
}

func TestAssistantContentEvents_ToolUseWithLeadingText(t *testing.T) {
	raw := json.RawMessage(`{"ToolUse": {
		"message_id": "m1",
		"content": "Let me check that file.",
		"tool_uses": [{"id": "t1", "name": "fs_read", "args": {}}]
	}}`)
	evs := assistantContentEvents(raw)
	require.Len(t, evs, 2)
	assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
	assert.Equal(t, "Let me check that file.", evs[0].Entry.Content)
	assert.Equal(t, agent.EntryTypeToolUse, evs[1].Entry.Type)
}

func TestAssistantContentEvents_EmptyResponseSkipped(t *testing.T) {
	evs := assistantContentEvents(json.RawMessage(`{"Response": {"message_id": "m1", "content": ""}}`))
	assert.Nil(t, evs)
}

func TestAssistantContentEvents_UnrecognizedVariantSkipped(t *testing.T) {
	evs := assistantContentEvents(json.RawMessage(`{"SomeFutureVariant": {}}`))
	assert.Nil(t, evs)
}

func TestDurationMs(t *testing.T) {
	cases := []struct {
		name  string
		start int64
		end   int64
		want  int
	}{
		{"real positive interval", 1784153260882, 1784153263710, 2828},
		{"zero start is unknown", 0, 100, 0},
		{"zero end is unknown", 100, 0, 0},
		{"end before start is unknown, never negative", 200, 100, 0},
		{"equal timestamps is a genuine zero-duration turn", 100, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := durationMs(requestMetadata{RequestStartTimestampMs: tc.start, StreamEndTimestampMs: tc.end})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNz(t *testing.T) {
	v := 42
	assert.Equal(t, 42, nz(&v))
	assert.Equal(t, 0, nz(nil))
}

// The locator's conversationID is Convert's own lookup key, known
// unconditionally for EVERY call — including a document that carries no
// session metadata of its own. Falling back to it unconditionally used to
// make sessionInfo non-nil for every conversation, defeating
// SessionInfoBuilder's "nil means nothing observed" contract. The fallback
// now only fills in the id once real metadata (here: ModelInfo) already
// proved the document itself has something worth a Session event for.
func TestSessionInfo_FallsBackToLocatorConversationIDOnlyWhenOtherMetadataFound(t *testing.T) {
	doc := &conversationDoc{ConversationID: "", ModelInfo: &modelInfo{ModelID: "claude-sonnet-4.5"}}
	info := sessionInfo("from-locator", doc)
	require.NotNil(t, info)
	assert.Equal(t, "from-locator", info.SessionID)
}

// The defect itself: a document with NO real session metadata at all must
// yield no Session event, regardless of what the locator's own conversation
// id happens to be — an empty doc.ConversationID is not, by itself, signal.
func TestSessionInfo_LocatorIDAloneIsNotEnoughToManufactureASession(t *testing.T) {
	doc := &conversationDoc{ConversationID: ""}
	info := sessionInfo("from-locator", doc)
	assert.Nil(t, info, "the locator id alone (with no other document metadata) must not manufacture a Session event")
}

func TestSessionInfo_NilWhenNothingFound(t *testing.T) {
	info := sessionInfo("", &conversationDoc{})
	assert.Nil(t, info, "a document with no session-level signal at all must yield no Session event, matching codex's scanSessionInfo contract")
}

func TestSessionInfo_ModelFallsBackToModelName(t *testing.T) {
	doc := &conversationDoc{ConversationID: "c1", ModelInfo: &modelInfo{ModelName: "claude-sonnet-4.5"}}
	info := sessionInfo("c1", doc)
	require.NotNil(t, info)
	assert.Equal(t, "claude-sonnet-4.5", info.Model)
}

// --- Locator ---

func TestLocator_RoundTrip(t *testing.T) {
	src := mustLocator(t, "/home/user/.local/share/kiro-cli/data.sqlite3", "74b27b3b-19e1-41f3-a8a6-bc8e227a3edb")
	dbPath, convID, err := parseLocator(src)
	require.NoError(t, err)
	assert.Equal(t, "/home/user/.local/share/kiro-cli/data.sqlite3", dbPath)
	assert.Equal(t, "74b27b3b-19e1-41f3-a8a6-bc8e227a3edb", convID)
}

func TestParseLocator_NoSeparator(t *testing.T) {
	_, _, err := parseLocator("/no/separator/here")
	require.Error(t, err)
}

func TestParseLocator_EmptyDBPath(t *testing.T) {
	_, _, err := parseLocator("#conv-id")
	require.Error(t, err)
}

func TestParseLocator_EmptyConversationID(t *testing.T) {
	_, _, err := parseLocator("/db/path#")
	require.Error(t, err)
}

// TestParseLocator_DBPathContainingHash exercises the LAST-'#'-split
// convention documented on locatorSep: a (POSIX-legal, if unusual) db path
// containing its own '#' must not be misparsed, so long as the real
// separator is the LAST one in the string.
func TestParseLocator_DBPathContainingHash(t *testing.T) {
	src := "/weird#path/data.sqlite3#conv-id"
	dbPath, convID, err := parseLocator(src)
	require.NoError(t, err)
	assert.Equal(t, "/weird#path/data.sqlite3", dbPath)
	assert.Equal(t, "conv-id", convID)
}

// --- EnumerateConversations ---

func TestEnumerateConversations(t *testing.T) {
	dbPath := insertFixtureRow(t, fixtureRow{
		Key: "/tmp/workdir-a", ConversationID: "conv-older",
		CreatedAt: 1000, UpdatedAt: 1000,
		Value: json.RawMessage(`{"conversation_id":"conv-older","history":[]}`),
	})
	addFixtureRow(t, dbPath, fixtureRow{
		Key: "/tmp/workdir-b", ConversationID: "conv-newer",
		CreatedAt: 2000, UpdatedAt: 2000,
		Value: json.RawMessage(`{"conversation_id":"conv-newer","history":[]}`),
	})

	refs, err := EnumerateConversations(context.Background(), dbPath)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "conv-older", refs[0].ConversationID, "oldest UpdatedAt first")
	assert.Equal(t, "/tmp/workdir-a", refs[0].WorkDir)
	assert.Equal(t, time.UnixMilli(1000).UTC(), refs[0].UpdatedAt)
	assert.Equal(t, "conv-newer", refs[1].ConversationID)

	// A ref's own fields must build a locator Convert can actually read.
	src := mustLocator(t, dbPath, refs[0].ConversationID)
	gotDBPath, gotConvID, err := parseLocator(src)
	require.NoError(t, err)
	assert.Equal(t, dbPath, gotDBPath)
	assert.Equal(t, refs[0].ConversationID, gotConvID)
}

func TestEnumerateConversations_OpenFailure(t *testing.T) {
	_, err := EnumerateConversations(context.Background(), filepath.Join(t.TempDir(), "missing.sqlite3"))
	require.Error(t, err)
}

func TestEnumerateConversations_EmptyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.sqlite3")
	// Reuse insertFixtureRow's CREATE TABLE via a zero-row insert-then-delete
	// would be awkward; build the empty table directly the same way
	// insertFixtureRow does, minus the INSERT.
	db := mustOpenAndCreateTable(t, dbPath)
	_ = db.Close()

	refs, err := EnumerateConversations(context.Background(), dbPath)
	require.NoError(t, err)
	assert.Empty(t, refs, "an empty table must yield an empty (not nil-panicking) slice")
}
