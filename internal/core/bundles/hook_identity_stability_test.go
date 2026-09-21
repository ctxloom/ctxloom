package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// hookIdentityBaseline is the trust identity every hook of a fully-populated
// bundle enumerated to BEFORE turn_start joined the vocabulary: each ref's
// event/index component and the content hash a trust grant binds to, in
// Entries() order. It was captured by running the enumeration at that tip and
// is pinned as a LITERAL so nothing in the current code can recompute it into
// agreement.
//
// It exists because hookEventOrder is a hook's trust identity in two ways:
// the ref "<bundle>#hooks/<event>/<index>" (per event, so a new event cannot
// renumber another's hooks) and the ORDER every hook report and baseline
// walks. A new event must therefore be APPENDED to hookEventOrder — never
// slotted in beside its sibling — so that every previously-granted ref keeps
// its hash and its position, and a signed bundle's review reads the same as
// the day it was approved. This test is what says so; the next event added
// extends the baseline rather than editing it.
var hookIdentityBaseline = []struct{ id, hash string }{
	{"pre_tool/0", "sha256:d9ac755f98b334555d39770523c111b35dd2fe1466af58ca2eee28841316b231"},
	{"post_tool/0", "sha256:1823bd0893e89e92ce01f90b5ac702f17ec5150bd4e4daf02bddee132a10c932"},
	{"session_start/0", "sha256:e70d0985f0bd57da07eb2554bc555753d62ea791de981d9126ae92b763968ebb"},
	{"session_end/0", "sha256:068f39265198639511451235f38b613832aaab0e66c32584a612c2e8c3334655"},
	{"pre_shell/0", "sha256:b9637a1f5a2e2b7d8809bbb6b18b0f53d2cd049f4c1a352f6178ce2add8b4d11"},
	{"post_file_edit/0", "sha256:5175abbbfe9e65617471fb5f9e9f5609008cc9be7edc069a2d7e410ced6c8fba"},
	{"turn_end/0", "sha256:752d7c15196616d466ab4b473c6c016f20adb6de33570bdc1b606fde74fb37fd"},
}

// The bundle the baseline was captured from: one hook per event, declared in
// a deliberately scrambled order so the enumeration order under test is
// hookEventOrder's and not the document's.
const hookIdentityDoc = "pre_tool:\n  - command: pt\nturn_end:\n  - command: te\nsession_start:\n  - command: ss\n" +
	"session_end:\n  - command: se\npost_tool:\n  - command: pot\npre_shell:\n  - command: psh\npost_file_edit:\n  - command: pfe\n"

// TestBundleHooks_TrustIdentityIsStableUnderVocabularyGrowth: every hook that
// had a trust identity before the vocabulary grew keeps it — same ref, same
// hash, same position — and the event that joined enumerates AFTER all of
// them.
func TestBundleHooks_TrustIdentityIsStableUnderVocabularyGrowth(t *testing.T) {
	var h BundleHooks
	require.NoError(t, yaml.Unmarshal([]byte(hookIdentityDoc+"turn_start:\n  - command: ts\n"), &h))

	entries := h.Entries()
	require.GreaterOrEqual(t, len(entries), len(hookIdentityBaseline)+1,
		"every baselined event plus the one that joined must enumerate")
	for i, want := range hookIdentityBaseline {
		assert.Equal(t, want.id, entries[i].ID(), "position %d: a baselined ref moved", i)
		assert.Equal(t, want.hash, entries[i].Hook.ComputeContentHash(), "position %d: a baselined hash changed", i)
	}
	assert.Equal(t, HookEventTurnStart+"/0", entries[len(hookIdentityBaseline)].ID(),
		"the event that joined the vocabulary enumerates after every baselined one — appended, not slotted in")
}
