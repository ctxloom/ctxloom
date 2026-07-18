package importer

import (
	"encoding/json"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// NonEmptyRaw normalizes a zero-length json.RawMessage to nil so
// agent.SessionEntry.ToolInput's omitempty (record.go's EntryPayload.
// ToolInput) actually omits it, rather than round-tripping an
// empty-but-non-nil slice that json.RawMessage's own MarshalJSON would
// otherwise turn into a literal `null`. Every engine that builds a tool_use
// entry needs this same normalization on its own arguments field, whatever
// shape that field arrives in vendor-side (codex's is a JSON-encoded
// STRING wrapping an object; claude's and kiro's are already bare JSON
// objects) — the vendor-specific unwrap stays in each adapter, only this
// final "empty means nil" step is shared.
func NonEmptyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// TextEntry wraps non-empty text as a single-element ChatEvent slice typed
// entryType, or nil for empty text — the "zero or one canonical entries from
// this buffered/joined text" shape codex, claude, antigravity, and kiro all
// repeat for user/assistant/thinking content: a turn with no visible text
// (an empty reasoning.summary, a step whose wrapper wasn't recognized, a
// content array with no text block) contributes nothing, never a
// zero-length entry a consumer would have to filter out itself.
func TextEntry(entryType agent.SessionEntryType, text string) []agent.ChatEvent {
	if text == "" {
		return nil
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: entryType, Content: text}}}
}

// ToolUseEvent builds the canonical tool_use entry every engine emits for a
// model-issued tool call: its name, the engine-native call id that later
// correlates it with a tool_result, and its arguments as already-valid JSON
// (NonEmptyRaw applied here so every caller gets the same omitempty-safe nil
// normalization for free instead of repeating the check itself).
func ToolUseEvent(name, callID string, input json.RawMessage) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolUse,
		ToolName:   name,
		ToolInput:  NonEmptyRaw(input),
		ToolCallID: callID,
	}}
}

// ToolResultEvent builds the canonical tool_result entry every engine emits
// for a tool call's outcome: the call id it answers, the flattened output
// text, and whether the vendor itself reported an error (never guessed —
// each adapter decides its own IsError value; some vendors have no such
// signal at all and must honestly pass false, see codex's
// functionCallOutputEvents).
func ToolResultEvent(callID, output string, isError bool) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolOutput: output,
		ToolCallID: callID,
		IsError:    isError,
	}}
}
