package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestMessageRouting_NeverEmitsAnUnspecifiedChannel pins the property that is
// currently keeping a latent wire-contract disagreement harmless.
//
// MESSAGE_CHANNEL_UNSPECIFIED (0) is read with OPPOSITE meaning on the two
// sides of this wire:
//
//   - coord/children.go accumulateFinalText and cli/run_owned.go admit only
//     MESSAGE_CHANNEL_FINAL, so a 0 channel is "not the agent's answer" and
//     its text is dropped from the parent's turn output entirely;
//   - operations/sessionfeed.go entryTypeFromRoute falls through to
//     `role == ASSISTANT` with no channel test, so ASSISTANT plus a 0 channel
//     IS assistant narrative and renders as the answer.
//
// Nothing has gone wrong yet for exactly one reason: messageRouting is the
// only production producer of this field and its default arm returns LOG, so
// 0 never reaches the wire. That reason is load-bearing and nothing asserted
// it — a new arm returning the zero value would silently make the same frame
// mean two different things in two packages.
//
// This test does NOT decide what 0 should mean; that is a wire-contract
// question about the enum itself and is escalated with the row. It pins the
// precondition under which the question stays theoretical.
func TestMessageRouting_NeverEmitsAnUnspecifiedChannel(t *testing.T) {
	// Every SessionEntryType the transcript vocabulary defines
	// (shared/agent/backend.go), plus a value from outside it to cover the
	// default arm, which is what an unknown engine entry takes.
	types := []agent.SessionEntryType{
		agent.EntryTypeUser,
		agent.EntryTypeAssistant,
		agent.EntryTypeThinking,
		agent.EntryTypeToolUse,
		agent.EntryTypeToolResult,
		agent.EntryTypeSystem,
		agent.SessionEntryType("some-entry-type-added-later"),
		agent.SessionEntryType(""),
	}
	for _, et := range types {
		role, channel := messageRouting(et)
		assert.NotEqual(t, agentcoordpb.MessageChannel_MESSAGE_CHANNEL_UNSPECIFIED, channel,
			"entry type %q routed to an UNSPECIFIED channel: readers of this field disagree "+
				"about what 0 means, so emitting it makes one frame mean two things", et)
		assert.NotEqual(t, agentcoordpb.MessageRole_MESSAGE_ROLE_UNSPECIFIED, role,
			"entry type %q routed to an UNSPECIFIED role", et)
	}
}
