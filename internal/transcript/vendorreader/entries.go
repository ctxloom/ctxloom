package vendorreader

import (
	"encoding/json"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// nonEmptyRaw normalizes a zero-length json.RawMessage to nil so that "this
// tool call had no arguments" has exactly ONE in-memory spelling flowing out
// of the adapters, whichever spelling a vendor's own unwrap happened to
// produce.
//
// It is not rescuing the marshaller. Measured on the canonical write path
// (entries_rawnorm_test.go): EntryPayload.ToolInput carries omitempty, so a
// zero-length value — nil or not — is omitted from the line entirely; and a
// RawMessage renders as the literal `null` only when it is NIL and the field
// has no omitempty, while an empty-but-non-nil one is not valid JSON at all
// and makes json.Marshal ERROR. So an empty non-nil slice never becomes a
// `null` on any path this package writes.
//
// What the normalization does buy is comparability upstream of the writer:
// nil and json.RawMessage{} are not equal, so without it a caller comparing,
// diffing or switching on ToolInput would see two different values for the
// same fact. Every engine that builds a tool_use entry needs it on its own
// arguments field, whatever shape that field arrives in vendor-side (codex's
// is a JSON-encoded STRING wrapping an object; claude's and kiro's are
// already bare JSON objects) — the vendor-specific unwrap stays in each
// adapter, only this final "empty means nil" step is shared. Unexported:
// ToolUseEvent, 26 lines below in this same file, is its only caller.
func nonEmptyRaw(raw json.RawMessage) json.RawMessage {
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
// (nonEmptyRaw applied here so every caller gets the same omitempty-safe nil
// normalization for free instead of repeating the check itself).
func ToolUseEvent(name, callID string, input json.RawMessage) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolUse,
		ToolName:   name,
		ToolInput:  nonEmptyRaw(input),
		ToolCallID: callID,
	}}
}

// ToolResultEvent builds the canonical tool_result entry every engine emits
// for a tool call's outcome: the call id it answers, the flattened output
// text, and whether the vendor itself reported an error (never guessed —
// each adapter decides its own IsError value; some vendors have no such
// signal at all and must honestly pass false, see codex's
// functionCallOutputEvents).
// content carries the STRUCTURED elements behind that flattened text, when
// the adapter could recover them. It is a separate parameter rather than a
// second constructor because there is exactly one canonical tool_result
// shape: an adapter with nothing structured to report passes nil and says so
// honestly, instead of the two near-identical constructors that would
// otherwise drift.
func ToolResultEvent(callID, output string, isError bool, content []agent.ToolContentBlock) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:        agent.EntryTypeToolResult,
		ToolOutput:  output,
		ToolCallID:  callID,
		IsError:     isError,
		ToolContent: content,
	}}
}
