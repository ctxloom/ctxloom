package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// TestParseBundle_MigratesPromptsKeyToCommands pins the on-load migration: a
// legacy bundle using the old top-level `prompts:` key loads its items into
// Commands rather than silently dropping them (the item-kind was renamed
// prompt→skill→command; the one-hop rewrite now retargets straight to
// `commands:`).
func TestParseBundle_MigratesPromptsKeyToCommands(t *testing.T) {
	old := []byte("version: \"1.0\"\nprompts:\n  review:\n    description: d\n    content: c\n")
	b, err := ParseBundle(old)
	require.NoError(t, err)
	require.Contains(t, b.Commands, "review", "legacy prompts: key must migrate into Commands")
	assert.Equal(t, "c", b.Commands["review"].Content)
	assert.Empty(t, b.Fragments, "no fragments declared")
}

// TestParseBundle_CommandsKeyUnchanged confirms a current bundle (already using
// `commands:`) loads unchanged — the migration is idempotent.
func TestParseBundle_CommandsKeyUnchanged(t *testing.T) {
	cur := []byte("version: \"1.0\"\ncommands:\n  review:\n    content: c\n")
	b, err := ParseBundle(cur)
	require.NoError(t, err)
	require.Contains(t, b.Commands, "review")
	assert.Equal(t, "c", b.Commands["review"].Content)
}

// TestParseBundle_CommandsWinsOverLegacyPrompts guards the both-keys-present
// edge: a bundle carrying both keys keeps the current `commands:` and drops
// the legacy `prompts:` rather than producing a duplicate-key parse error.
func TestParseBundle_CommandsWinsOverLegacyPrompts(t *testing.T) {
	both := []byte("version: \"1.0\"\ncommands:\n  new:\n    content: n\nprompts:\n  old:\n    content: o\n")
	b, err := ParseBundle(both)
	require.NoError(t, err)
	assert.Contains(t, b.Commands, "new")
	assert.NotContains(t, b.Commands, "old", "legacy prompts: must not override the current commands:")
}

// An entry under `skills:` carrying an inline `content:` (a command's shape)
// is refused by the strict decode, naming the key, rather than dropped.
func TestParseBundle_ContentShapedSkillIsRefused(t *testing.T) {
	_, err := ParseBundle([]byte("version: \"1.0\"\nskills:\n  humanize:\n    path: skills/humanize\n  review:\n    content: c\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "content")
}

// TestParseBundle_NewShapeSkillsKeyParsesAsSkill is the other half of the D1
// guard, now that Part B's real skill item-kind exists: an entry under
// `skills:` that does NOT carry `content:` (the legacy command shape) is a
// genuine Agent Skill package reference and must parse cleanly into
// Bundle.Skills, never error.
func TestParseBundle_NewShapeSkillsKeyParsesAsSkill(t *testing.T) {
	future := []byte("version: \"1.0\"\nskills:\n  humanize:\n    path: skills/humanize\n    tags: [writing]\n")
	b, err := ParseBundle(future)
	require.NoError(t, err)
	require.Contains(t, b.Skills, "humanize")
	assert.Equal(t, "skills/humanize", b.Skills["humanize"].Path)
	assert.Equal(t, []string{"writing"}, b.Skills["humanize"].Tags)
}

// TestParseBundle_NewShapeSkillsKeyDefaultsPath confirms a skill entry with no
// explicit `path:` still parses (the default skills/<name> resolution is a
// loader-time concern via ResolveSkillDir, not a parse-time requirement).
func TestParseBundle_NewShapeSkillsKeyDefaultsPath(t *testing.T) {
	minimal := []byte("version: \"1.0\"\nskills:\n  humanize:\n    notes: a note\n")
	b, err := ParseBundle(minimal)
	require.NoError(t, err)
	require.Contains(t, b.Skills, "humanize")
	assert.Empty(t, b.Skills["humanize"].Path)
	assert.Equal(t, "a note", b.Skills["humanize"].Notes)
}

// A bundle that is not YAML is its parse failure, not a version fault.
func TestParseBundle_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	_, err := ParseBundle([]byte("fragments: [unterminated\n"))
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
