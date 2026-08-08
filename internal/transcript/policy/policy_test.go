package policy

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func toolResult(blocks ...agent.ToolContentBlock) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:        agent.EntryTypeToolResult,
		ToolOutput:  "flattened text",
		ToolContent: blocks,
	}}
}

// TestDefault_ExcludesExactlyTheTwoApprovedKinds pins the whole default
// policy. It asserts the KEPT kinds as forcefully as the excluded ones,
// because a policy that excludes too much is the more expensive failure and
// the cheaper one to introduce by accident.
func TestDefault_ExcludesExactlyTheTwoApprovedKinds(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		wantKept   bool
		wantReason string
	}{
		{agent.KindProcessOutput, false, ReasonRedundant},
		{agent.KindFileSnapshot, false, ReasonBloat},
		{agent.KindContent, true, ""},
		{agent.KindToolCatalog, true, ""},
		{agent.KindAgentResult, true, ""},
		{agent.KindDiff, true, ""},
		{agent.KindTerminal, true, ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			in := agent.ToolContentBlock{Kind: tc.kind, Raw: json.RawMessage(`{"a":1}`)}
			got := Default().Apply(toolResult(in))
			require.Len(t, got.Entry.ToolContent, 1, "a block is replaced, never removed")
			out := got.Entry.ToolContent[0]

			if tc.wantKept {
				assert.Equal(t, tc.kind, out.Kind, "kept blocks must pass through untouched")
				assert.JSONEq(t, `{"a":1}`, string(out.Raw))
				return
			}
			assert.Equal(t, agent.KindExcluded, out.Kind)
			assert.Contains(t, string(out.Raw), tc.wantReason,
				"the marker must record WHY, not merely that something is gone")
			assert.Contains(t, string(out.Raw), tc.kind,
				"the marker must record WHAT kind was withheld")
		})
	}
}

// TestApply_ExclusionIsNeverSilent is the anti-regression for the defect this
// whole package exists to fix. An excluded element must leave a typed marker:
// if it were merely removed, a reader could not tell "policy withheld this"
// from "the tool returned nothing" — and 385 real tool_result entries
// flattening to empty, indistinguishable from a tool that returned nothing,
// is exactly what motivated the parse-totally/filter-deliberately split.
func TestApply_ExclusionIsNeverSilent(t *testing.T) {
	raw := json.RawMessage(`{"stdout":"0123456789","stderr":""}`)
	got := Default().Apply(toolResult(agent.ToolContentBlock{
		Kind: agent.KindProcessOutput, Raw: raw,
	}))

	require.Len(t, got.Entry.ToolContent, 1, "the block count must not shrink")
	out := got.Entry.ToolContent[0]
	assert.Equal(t, agent.KindExcluded, out.Kind)
	assert.NotEmpty(t, out.Text, "the marker must be legible to a human reader")
	assert.Contains(t, out.Text, strconv.Itoa(len(raw)), "the marker must state how many bytes were withheld")
	assert.NotContains(t, string(out.Raw), "0123456789",
		"the withheld payload itself must not survive in the marker")
}

// TestApply_DoesNotMutateTheCallersEvent guards a real aliasing hazard:
// ChatEvent.Entry is a POINTER, so rewriting ToolContent in place would edit
// the event the caller still holds — and the live chat path hands the same
// event to more than one consumer.
func TestApply_DoesNotMutateTheCallersEvent(t *testing.T) {
	in := toolResult(agent.ToolContentBlock{
		Kind: agent.KindProcessOutput, Raw: json.RawMessage(`{"stdout":"x"}`),
	})
	_ = Default().Apply(in)

	require.Len(t, in.Entry.ToolContent, 1)
	assert.Equal(t, agent.KindProcessOutput, in.Entry.ToolContent[0].Kind,
		"the caller's own event must be untouched")
}

func TestApply_PassesThroughEventsWithNoToolContent(t *testing.T) {
	text := agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "hello",
	}}
	assert.Equal(t, text, Default().Apply(text))

	// A zero event (no Entry at all) must not panic: Session and Complete
	// records reach the recorder as ChatEvents with no Entry.
	assert.NotPanics(t, func() { _ = Default().Apply(agent.ChatEvent{}) })
}

// TestApply_MixedContentKeepsOrder pins that filtering is positional: a
// withheld element is replaced where it stood, so the surviving elements keep
// their encounter order relative to it.
func TestApply_MixedContentKeepsOrder(t *testing.T) {
	got := Default().Apply(toolResult(
		agent.ToolContentBlock{Kind: agent.KindContent, Text: "before"},
		agent.ToolContentBlock{Kind: agent.KindProcessOutput, Raw: json.RawMessage(`{"stdout":"x"}`)},
		agent.ToolContentBlock{Kind: agent.KindContent, Text: "after"},
	))

	require.Len(t, got.Entry.ToolContent, 3)
	assert.Equal(t, "before", got.Entry.ToolContent[0].Text)
	assert.Equal(t, agent.KindExcluded, got.Entry.ToolContent[1].Kind)
	assert.Equal(t, "after", got.Entry.ToolContent[2].Text)
}

// TestApply_FirstRejectingRuleWins drives Apply through a rule set this test
// controls, so the ordering contract is pinned independently of whatever
// Default() happens to decide today.
func TestApply_FirstRejectingRuleWins(t *testing.T) {
	p := New(
		func(agent.ToolContentBlock) (bool, string) { return false, "first" },
		func(agent.ToolContentBlock) (bool, string) { return false, "second" },
	)
	got := p.Apply(toolResult(agent.ToolContentBlock{Kind: agent.KindContent}))
	require.Len(t, got.Entry.ToolContent, 1)
	assert.Contains(t, string(got.Entry.ToolContent[0].Raw), "first")
}
