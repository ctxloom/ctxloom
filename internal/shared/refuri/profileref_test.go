package refuri

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProfileSelector pins the "<bundle>#profiles/<name>" grammar — the profile
// counterpart to remote.FragmentSelector ("#fragments/") and remote.CommandSelector
// ("#commands/") — so bundle profiles are addressed consistently with the other
// bundle item kinds.
func TestProfileSelector(t *testing.T) {
	assert.Equal(t, "#profiles/", ProfileSelector)
}
