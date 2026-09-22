package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChatEvent_ExactlyOneVariant documents that a ChatEvent carries exactly one
// of Entry / Complete / Session.
func TestChatEvent_ExactlyOneVariant(t *testing.T) {
	for _, ev := range []ChatEvent{
		{Entry: &SessionEntry{}},
		{Complete: &TurnMeta{}},
		{Session: &ChatSessionInfo{}},
	} {
		set := 0
		if ev.Entry != nil {
			set++
		}
		if ev.Complete != nil {
			set++
		}
		if ev.Session != nil {
			set++
		}
		assert.Equal(t, 1, set, "exactly one ChatEvent variant must be set")
	}
}

// TestChatEvent_Kind names the port-level kind every driver relays: the
// session, the completion, an entry's own type, and "event" for a raw-only
// frame that has no other shape.
func TestChatEvent_Kind(t *testing.T) {
	assert.Equal(t, "session", ChatEvent{Session: &ChatSessionInfo{}}.Kind())
	assert.Equal(t, "complete", ChatEvent{Complete: &TurnMeta{}}.Kind())
	assert.Equal(t, "assistant", ChatEvent{Entry: &SessionEntry{Type: EntryTypeAssistant}}.Kind())
	assert.Equal(t, "event", ChatEvent{Raw: []byte(`{}`)}.Kind())
}
