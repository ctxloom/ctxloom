package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// writtenHooks delivers cfg through the settings writer and reads back the
// hooks object from the BYTES of .claude/settings.json — never the writer's
// return value, since a writer that reports success and writes nothing is
// this codebase's characteristic bug.
func writtenHooks(t *testing.T, cfg *wire.HooksConfig) map[string]any {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, (&ClaudeCodeHookWriter{}).WriteSettings(cfg, ctxloomBundleMCP(), tmpDir))
	raw, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	hooks, ok := settings["hooks"].(map[string]any)
	require.True(t, ok, "settings carry a hooks object")
	return hooks
}

// TestClaudeCodeHookWriter_TurnStartReachesUserPromptSubmit: the unified
// turn_start event lands in claude-code's native UserPromptSubmit event — the
// event claude fires when a prompt is submitted and before the model runs,
// whose hook stdout becomes the turn's own context. No matcher: there is no
// tool to match against.
func TestClaudeCodeHookWriter_TurnStartReachesUserPromptSubmit(t *testing.T) {
	hooks := writtenHooks(t, &wire.HooksConfig{Unified: wire.UnifiedHooks{
		TurnStart: []wire.Hook{{Command: "ctxloom hook mail-drain", Type: "command", Timeout: 15}},
	}})

	groups, ok := hooks["UserPromptSubmit"].([]any)
	require.True(t, ok, "turn_start must be written under claude-code's UserPromptSubmit event, got events %v", keysOf(hooks))
	require.Len(t, groups, 1)
	group := groups[0].(map[string]any)
	_, hasMatcher := group["matcher"]
	assert.False(t, hasMatcher, "UserPromptSubmit takes no matcher")
	entries := group["hooks"].([]any)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	assert.Equal(t, "ctxloom hook mail-drain", entry["command"])
	assert.Equal(t, "command", entry["type"])
}

// TestClaudeCodeHookWriter_EveryUnifiedEventIsRouted pins the hand-written
// route table in addUnifiedHooks against the struct it routes: a field added
// to wire.UnifiedHooks and missed there compiles, delivers, and is silently
// never written — the event would exist everywhere except in the one file the
// engine reads. Reflection over the struct is the enumeration that cannot go
// stale.
func TestClaudeCodeHookWriter_EveryUnifiedEventIsRouted(t *testing.T) {
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			var u wire.UnifiedHooks
			cmd := "routed-" + name
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]wire.Hook{{Command: cmd, Type: "command"}}))

			hooks := writtenHooks(t, &wire.HooksConfig{Unified: u})
			raw, err := json.Marshal(hooks)
			require.NoError(t, err)
			assert.Contains(t, string(raw), cmd,
				"addUnifiedHooks routes %s nowhere — the hook is delivered and never written", name)
		})
	}
}

// TestHooks_DecodesUserPromptSubmitAsTurnStart: the codec's native→unified
// table carries the new event in both directions, so a hook fired by claude
// on UserPromptSubmit reports as turn_start.
func TestHooks_DecodesUserPromptSubmitAsTurnStart(t *testing.T) {
	codec := claudeKind(t).Hooks()
	ev, err := codec.Decode("UserPromptSubmit", []byte(`{"session_id":"s1","hook_event_name":"UserPromptSubmit","transcript_path":"/t/s1.jsonl"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{Event: "turn_start", NativeSession: "s1", Transcript: "/t/s1.jsonl"}, ev)
	assert.Equal(t, "UserPromptSubmit", hookEventMap()["turn_start"], "Exports names the native event turn_start registers under")
}

// TestClaudeCodeHookWriter_ApprovalHooksReachPermissionRequestAndPreToolUse:
// the approval hooks a human-approved run is delivered land where claude
// spawns them — the permission ask under PermissionRequest for every tool,
// the question/plan hook under PreToolUse for exactly those two tools —
// each with a timeout that outlives the approval timeout.
func TestClaudeCodeHookWriter_ApprovalHooksReachPermissionRequestAndPreToolUse(t *testing.T) {
	approval := agent.ApprovalHooks(15 * time.Minute)
	hooks := writtenHooks(t, &wire.HooksConfig{Unified: approval})

	for event, want := range map[string]wire.Hook{"PermissionRequest": approval.PermissionAsk[0], "PreToolUse": approval.PreTool[0]} {
		groups, ok := hooks[event].([]any)
		require.True(t, ok, "%s must be written, got events %v", event, keysOf(hooks))
		require.Len(t, groups, 1, event)
		group := groups[0].(map[string]any)
		matcher, _ := group["matcher"].(string)
		assert.Equal(t, want.Matcher, matcher, event)
		entries := group["hooks"].([]any)
		require.Len(t, entries, 1, event)
		entry := entries[0].(map[string]any)
		assert.Equal(t, want.Command, entry["command"], event)
		assert.EqualValues(t, want.Timeout, entry["timeout"], event)
	}
}
