package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseBundle_MigratesPromptsKeyToSkills pins the on-load migration: a legacy
// bundle using the old top-level `prompts:` key loads its items into Skills
// rather than silently dropping them (the item-kind was renamed prompt→skill).
func TestParseBundle_MigratesPromptsKeyToSkills(t *testing.T) {
	old := []byte("name: legacy\nversion: \"1.0\"\nprompts:\n  review:\n    description: d\n    content: c\n")
	b, err := ParseBundle(old)
	require.NoError(t, err)
	require.Contains(t, b.Skills, "review", "legacy prompts: key must migrate into Skills")
	assert.Equal(t, "c", b.Skills["review"].Content)
	assert.Empty(t, b.Fragments, "no fragments declared")
}

// TestParseBundle_SkillsKeyUnchanged confirms a current bundle (already using
// `skills:`) loads unchanged — the migration is idempotent.
func TestParseBundle_SkillsKeyUnchanged(t *testing.T) {
	cur := []byte("name: cur\nversion: \"1.0\"\nskills:\n  review:\n    content: c\n")
	b, err := ParseBundle(cur)
	require.NoError(t, err)
	require.Contains(t, b.Skills, "review")
	assert.Equal(t, "c", b.Skills["review"].Content)
}

// TestParseBundle_SkillsWinsOverLegacyPrompts guards the both-keys-present edge:
// a bundle carrying both keys keeps the current `skills:` and drops the legacy
// `prompts:` rather than producing a duplicate-key parse error.
func TestParseBundle_SkillsWinsOverLegacyPrompts(t *testing.T) {
	both := []byte("name: both\nversion: \"1.0\"\nskills:\n  new:\n    content: n\nprompts:\n  old:\n    content: o\n")
	b, err := ParseBundle(both)
	require.NoError(t, err)
	assert.Contains(t, b.Skills, "new")
	assert.NotContains(t, b.Skills, "old", "legacy prompts: must not override the current skills:")
}
