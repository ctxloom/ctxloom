package agent

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "denied", ChatEvent{Denied: &PermissionDenial{}}.Kind())
	assert.Equal(t, "event", ChatEvent{Raw: []byte(`{}`)}.Kind())
}

// TestDecider_String names who decided a denial, as the parent's blocked
// report spells it; an out-of-range value is visibly bad.
func TestDecider_String(t *testing.T) {
	for d, want := range map[Decider]string{
		DeciderPolicy:      "policy",
		DeciderHuman:       "human",
		DeciderTimeout:     "timeout",
		DeciderCancelled:   "cancelled",
		DeciderRefused:     "refused",
		DeciderPlanPosture: "plan posture",
		DeciderGrant:       "grant",
	} {
		assert.Equal(t, want, d.String())
	}
	assert.Equal(t, DeciderPolicy, Decider(0), "the zero value is the engine's own policy: an engine-only denial needs no join")
	assert.Equal(t, "decider(42)", Decider(42).String())
}

// TestDecider_PersistsByName: a decider crosses every persisted boundary as
// its name, and each member reads back as itself — through JSON, the form
// the transcript and `--format json` write.
func TestDecider_PersistsByName(t *testing.T) {
	for _, d := range []Decider{DeciderPolicy, DeciderHuman, DeciderTimeout, DeciderCancelled, DeciderRefused, DeciderPlanPosture, DeciderGrant} {
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.Equal(t, strconv.Quote(d.String()), string(raw), "a decider is written as its name")
		var back Decider
		require.NoError(t, json.Unmarshal(raw, &back))
		assert.Equal(t, d, back)
	}
}

// TestDecider_UnknownIsRefusedBothWays: a name this build does not know is an
// error on read — its zero is DeciderPolicy, a real answer, so defaulting
// would report "policy" for a decision nobody here can name — and a value
// outside the vocabulary is an error on write rather than a "decider(N)"
// that no reader could parse back.
func TestDecider_UnknownIsRefusedBothWays(t *testing.T) {
	var d Decider = DeciderHuman
	err := json.Unmarshal([]byte(`"committee"`), &d)
	require.ErrorIs(t, err, ErrUnknownDecider)
	assert.Equal(t, DeciderHuman, d, "a refused name leaves the destination untouched")

	var typeErr *json.UnmarshalTypeError
	require.ErrorAs(t, json.Unmarshal([]byte(`0`), &d), &typeErr, "the ordinal is not a name: a line from the int encoding does not decode")

	_, err = json.Marshal(Decider(42))
	require.ErrorIs(t, err, ErrUnknownDecider)
}
