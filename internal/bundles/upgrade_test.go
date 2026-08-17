package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestParseBundle_LegacySkillsKeyErrsLoud is the migration guard (D1, hard
// break): `skills:` is repurposed for a future Agent Skills item-kind (Part B)
// that never carries an inline `content:` field. An entry under `skills:`
// still shaped like the legacy command/prompt item — a scalar `content:` —
// must fail the load with a loud, actionable error rather than being silently
// dropped (default YAML unmarshal ignores unknown keys) or misparsed.
func TestParseBundle_LegacySkillsKeyErrsLoud(t *testing.T) {
	legacy := []byte("version: \"1.0\"\nskills:\n  review:\n    description: d\n    content: c\n")
	_, err := ParseBundle(legacy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skills:")
	assert.Contains(t, err.Error(), "commands:")
	assert.Contains(t, err.Error(), "review")
}

// TestParseBundle_NewShapeSkillsKeyParsesAsSkill is the other half of the D1
// guard, now that Part B's real skill item-kind exists: an entry under
// `skills:` that does NOT carry `content:` (the legacy command shape) is a
// genuine Agent Skill package reference and must parse cleanly into
// Bundle.Skills, never error. Shape alone (presence/absence of `content:`)
// is what detectLegacySkillsKey uses to tell the two apart deterministically.
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

// TestParseBundle_SkillsKeyLegacyEntryAmongNewShapeStillErrs guards the mixed
// case: a `skills:` block with one legacy (content-bearing) entry alongside a
// new-shape one must still fail loud on the legacy entry specifically — the
// new-shape sibling does not "vote" the legacy entry into being accepted.
func TestParseBundle_SkillsKeyLegacyEntryAmongNewShapeStillErrs(t *testing.T) {
	mixed := []byte("version: \"1.0\"\nskills:\n  humanize:\n    path: skills/humanize\n  oldcmd:\n    content: c\n")
	_, err := ParseBundle(mixed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oldcmd")
	assert.Contains(t, err.Error(), "commands:")
}

// TestParseBundle_LegacySkillsKeyErrorOnARealBundle pins that every other
// test of this guard hands ParseBundle a document with a root `name:` key —
// which is precisely why nobody noticed that a REAL bundle.yaml has no such
// key. Bundle.Name is `yaml:"-"`, so the marshaller never writes one and the
// unmarshaller never reads one; the root scan for it found nothing and the
// message opened with an empty identifier.
//
// The file identity belongs to the caller (LoadFile wraps this error with the
// path), so the message must carry the entry names and no dangling blank.
func TestParseBundle_LegacySkillsKeyErrorOnARealBundle(t *testing.T) {
	// No `name:` — the shape ctxloom itself writes.
	legacy := []byte("version: \"1.0\"\nskills:\n  review:\n    content: c\n")
	_, err := ParseBundle(legacy)
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "review", "the offending entry must be named")
	assert.Contains(t, msg, "commands:", "the remedy must be named")
	assert.NotContains(t, msg, "bundle :", "no empty identifier where a name would go")
	assert.NotContains(t, msg, "bundle:", "no empty identifier where a name would go")
}
