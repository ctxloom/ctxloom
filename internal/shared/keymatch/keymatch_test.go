package keymatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The calibration is the whole value of sharing this: two surfaces that answer
// "did you mean" differently are worse than one that answers it at all. These
// pin the budget both of them now inherit.
func TestNearest(t *testing.T) {
	bundleKeys := []string{"version", "tags", "author", "description", "notes", "installation", "fragments", "commands", "mcp", "skills", "profiles", "hooks"}

	for _, tc := range []struct {
		name string
		key  string
		want string
		why  string
	}{
		{"one transposed rune", "hoooks", "hooks", "a doubled letter is the archetypal typo and must be caught"},
		{"one dropped rune", "fragmets", "fragments", "an omitted letter must be caught"},
		{"one wrong rune", "profils", "profiles", "the case the config loader was built for"},
		{"exact match", "hooks", "hooks", "a key that IS known is distance zero from itself"},
		{"nothing near", "lifecycle", "", "an unrelated word must produce no suggestion rather than a confusing one"},
		{"short and unrelated", "ui", "", "a short key must not match an unrelated short key — the length term exists for this"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Nearest(tc.key, bundleKeys), tc.why)
		})
	}
}

// No candidates is not a crash and not a false suggestion.
func TestNearest_EmptyKnownSet(t *testing.T) {
	assert.Empty(t, Nearest("anything", nil))
	assert.Empty(t, Nearest("", nil))
}
