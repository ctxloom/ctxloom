package vendorreader

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// withOmitempty mirrors the canonical write path's field shape
// (transcript.EntryPayload.ToolInput, record.go:127).
type withOmitempty struct {
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
}

// withoutOmitempty is the same field WITHOUT the tag, i.e. what the canonical
// path would look like if the tag were ever dropped.
type withoutOmitempty struct {
	ToolInput json.RawMessage `json:"tool_input"`
}

// TestNonEmptyRaw_EmptyNonNilDoesNotBecomeNull measures the claim
// nonEmptyRaw's doc comment used to make. json.RawMessage.MarshalJSON returns
// the literal `null` for a NIL receiver; an empty-but-non-nil slice is not
// nil, and returns zero bytes — which is not valid JSON, so encoding/json
// ERRORS rather than emitting `null`. On the canonical write path the field
// carries omitempty, so a zero-length value never reaches the marshaller at
// all. Neither branch produces the `null` the old rationale warned about.
//
// This is the invariant the corrected comment now asserts, pinned so that
// changing either half (dropping omitempty, or Go changing RawMessage's
// behaviour) re-opens the question instead of leaving prose nobody can check.
func TestNonEmptyRaw_EmptyNonNilDoesNotBecomeNull(t *testing.T) {
	empty := json.RawMessage{}
	require.NotNil(t, empty, "fixture must be empty but NOT nil")
	require.Len(t, empty, 0)

	// With omitempty — the canonical shape — the field is omitted outright.
	out, err := json.Marshal(withOmitempty{ToolInput: empty})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(out), "a zero-length value is omitted, never rendered")

	// Without omitempty it is an ERROR, not a `null`.
	_, err = json.Marshal(withoutOmitempty{ToolInput: empty})
	require.Error(t, err, "an empty non-nil RawMessage is not marshallable at all")

	// `null` is what a NIL RawMessage produces, and only without omitempty.
	nilOut, err := json.Marshal(withoutOmitempty{ToolInput: nil})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool_input":null}`, string(nilOut))

	nilOmit, err := json.Marshal(withOmitempty{ToolInput: nil})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(nilOmit))
}

// TestNonEmptyRaw_CanonicalPathOmitsEitherWay closes the loop through the real
// writer types rather than local mirrors: whichever of nil or empty-non-nil a
// vendor adapter hands ToolUseEvent, the canonical line carries no tool_input
// key. That equivalence is the actual justification for the normalization —
// it keeps ONE spelling of "no arguments" flowing out of the adapters, rather
// than rescuing the marshaller from anything.
func TestNonEmptyRaw_CanonicalPathOmitsEitherWay(t *testing.T) {
	for name, input := range map[string]json.RawMessage{
		"nil":            nil,
		"empty_non_nil":  {},
		"zero_len_slice": make(json.RawMessage, 0),
	} {
		t.Run(name, func(t *testing.T) {
			ev := ToolUseEvent("Read", "call-1", input)
			require.NotNil(t, ev.Entry)
			assert.Nil(t, ev.Entry.ToolInput, "nonEmptyRaw normalizes every no-arguments spelling to nil")

			line, err := json.Marshal(transcript.EntryPayload{
				Type:       string(ev.Entry.Type),
				ToolName:   ev.Entry.ToolName,
				ToolInput:  ev.Entry.ToolInput,
				ToolCallID: ev.Entry.ToolCallID,
			})
			require.NoError(t, err)
			assert.NotContains(t, string(line), "tool_input",
				"the canonical line must carry no tool_input key at all")
			assert.NotContains(t, string(line), "null")
		})
	}
}
