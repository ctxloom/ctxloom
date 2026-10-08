package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// hookIdentityBaseline is the identity every hook of a fully-populated bundle
// enumerated to BEFORE turn_start joined the vocabulary, in Entries() order. It
// is pinned as a LITERAL so nothing in the current code can recompute it into
// agreement.
//
// hookEventOrder is a hook's identity in two ways: the ref
// "<bundle>#hooks/<event>/<index>" (per event, so a new event cannot renumber
// another's hooks) and the ORDER every hook report walks. A new event must
// therefore be APPENDED to hookEventOrder — never slotted in beside its sibling —
// so that every existing hook keeps its position and a report reads the same as
// it did before the vocabulary grew. The next event added extends the baseline
// rather than editing it.
var hookIdentityBaseline = []string{
	"pre_tool/0",
	"post_tool/0",
	"session_start/0",
	"session_end/0",
	"pre_shell/0",
	"post_file_edit/0",
	"turn_end/0",
}

// The bundle the baseline was captured from: one hook per event, declared in
// a deliberately scrambled order so the enumeration order under test is
// hookEventOrder's and not the document's.
const hookIdentityDoc = "pre_tool:\n  - command: pt\nturn_end:\n  - command: te\nsession_start:\n  - command: ss\n" +
	"session_end:\n  - command: se\npost_tool:\n  - command: pot\npre_shell:\n  - command: psh\npost_file_edit:\n  - command: pfe\n"

// TestBundleHooks_IdentityIsStableUnderVocabularyGrowth: every hook that had an
// identity before the vocabulary grew keeps it — same ref, same position — and
// the event that joined enumerates AFTER all of them.
func TestBundleHooks_IdentityIsStableUnderVocabularyGrowth(t *testing.T) {
	var h BundleHooks
	require.NoError(t, yaml.Unmarshal([]byte(hookIdentityDoc+"turn_start:\n  - command: ts\n"), &h))

	entries := h.Entries()
	require.Len(t, entries, len(hookIdentityBaseline)+1,
		"every baselined event plus the one that joined must enumerate")
	for i, want := range hookIdentityBaseline {
		assert.Equal(t, want, entries[i].ID(), "position %d: a baselined ref moved", i)
	}
	assert.Equal(t, HookEventTurnStart+"/0", entries[len(hookIdentityBaseline)].ID(),
		"the event that joined the vocabulary enumerates after every baselined one — appended, not slotted in")
}
