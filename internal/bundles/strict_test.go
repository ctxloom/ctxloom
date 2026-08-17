package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A key the Bundle schema does not model must be REFUSED, and the refusal must
// name it. Before strict decode this document loaded clean and shipped a
// bundle with no hooks at all — exit 0, a success message, and a lifecycle the
// author wrote that never fires.
func TestParseBundle_RejectsUnknownTopLevelKey(t *testing.T) {
	doc := []byte("version: \"1.0.0\"\nhoooks:\n  post_tool:\n    - command: echo hi\n")

	b, err := ParseBundle(doc)

	require.Error(t, err, "an unknown top-level key must fail the load, not be dropped")
	assert.Nil(t, b)
	assert.Contains(t, err.Error(), "hoooks", "the error must NAME the offending key — an author cannot fix a key they are not told")
	assert.Contains(t, err.Error(), "did you mean `hooks`?", "a near-miss must be suggested")
	assert.Contains(t, err.Error(), "line 2", "the error must locate the key in the document")
}

// The same rule inside a nested item. This exact typo is live in the published
// corpus (`note:` for `notes:` in an mcp entry of ctxloom-default's
// sequential-thinking bundle and ctxloom-personal's serena bundle), where it
// silently discards the author's prose — nested keys are where the silent drop
// actually happens, so strictness that stopped at the top level would miss it.
func TestParseBundle_RejectsUnknownKeyInsideAnItem(t *testing.T) {
	doc := []byte("version: \"1.0.0\"\nmcp:\n  thinking:\n    command: npx\n    note: dropped in silence\n")

	_, err := ParseBundle(doc)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "note")
	assert.Contains(t, err.Error(), "did you mean `notes`?")
	assert.Contains(t, err.Error(), "mcp.<name>", "the error must say WHERE the key would have been legal, not just name a Go type")
}

// ORDERING GUARD. bundleUpgrades runs over the raw document BEFORE the strict
// decode, so a bundle still carrying a legacy key is migrated rather than
// refused. Reverse the two and every bundle written before the prompts ->
// commands rename hard-fails on load instead of upgrading — strictness would
// become a breaking change for every older bundle on disk.
//
// The assertion is on the PAYLOAD, not merely on the absence of an error: a
// migration that "succeeds" while dropping the item is the silent no-op this
// whole file exists to refuse.
func TestParseBundle_LegacyPromptsKeyIsMigratedBeforeStrictDecodeRefusesIt(t *testing.T) {
	legacy := []byte("version: \"1.0.0\"\nprompts:\n  review:\n    content: review this\n    description: a legacy prompt\n")

	b, err := ParseBundle(legacy)

	require.NoError(t, err, "a legacy `prompts:` bundle must still load — the upgrade pipeline runs before strictness")
	require.Contains(t, b.Commands, "review", "the migrated item must actually be present, not merely not-an-error")
	assert.Equal(t, "review this", b.Commands["review"].Content)
	assert.Empty(t, b.Fragments, "nothing else may be invented by the migration")
}

// Strictness must not swallow the empty-document rule. An empty or truncated
// file has no unknown keys, so it reaches declaresNothing exactly as before and
// must still fail with the empty-bundle diagnostic rather than a decode error.
func TestParseBundle_StrictDecodeLeavesTheEmptyDocumentRuleIntact(t *testing.T) {
	for _, doc := range []string{"", "   \n", "# just a comment\n", "---\n", "null\n", "{}\n"} {
		_, err := ParseBundle([]byte(doc))

		require.Error(t, err, "empty document %q", doc)
		assert.Contains(t, err.Error(), "bundle is empty", "the empty-document rule owns this case, not the strict decoder: %q", doc)
	}
}

// The other half of strictness: every key the schema DOES model must still be
// accepted. A strict decoder that rejects a valid key is worse than a lenient
// one, and this is the test that fails when a field is added to Bundle without
// its yaml tag.
func TestParseBundle_AcceptsEveryKeyTheSchemaModels(t *testing.T) {
	doc := []byte(`version: "1.0.0"
tags: [a]
author: someone
description: d
notes: n
installation: i
fragments:
  f:
    content: c
    tags: [t]
    notes: n
    installation: i
    content_hash: h
    distilled: d
    distilled_by: model
    no_distill: true
commands:
  c:
    description: d
    content: body
mcp:
  m:
    command: npx
    args: [-y, thing]
    env: {K: V}
    notes: n
    installation: i
    content_hash: h
skills:
  s:
    path: skills/s
    tags: [t]
    notes: n
profiles:
  p:
    description: d
    parents: [base]
    bundles: [other]
hooks:
  post_file_edit:
    - command: echo hi
      matcher: "*.go"
`)

	b, err := ParseBundle(doc)

	require.NoError(t, err, "a document using the full modeled vocabulary must load")
	assert.Len(t, b.Fragments, 1)
	assert.Len(t, b.Commands, 1)
	assert.Len(t, b.MCP, 1)
	assert.Len(t, b.Skills, 1)
	assert.Len(t, b.Profiles, 1)
	assert.True(t, b.Hooks.HasAny())
}

// The derived key vocabulary must actually be derived. A hand-written list
// drifts the moment a field is added, and the drift shows up as an error
// message telling an author their correct key is invalid.
func TestBundleKeySites_AreDerivedFromTheStructs(t *testing.T) {
	sites := bundleKeySites()

	root := sites["bundles.Bundle"]
	assert.Equal(t, "the top level of the bundle", root.where)
	for _, key := range []string{"version", "fragments", "commands", "skills", "mcp", "profiles", "hooks"} {
		assert.Contains(t, root.keys, key, "declaresNothing's vocabulary must be spellable at the top level")
	}
	assert.NotContains(t, root.keys, "path", "a yaml:\"-\" field is not settable from a document, so it is not a valid key")
	assert.NotContains(t, root.keys, "signer", "the signer field is unexported and unsettable — offering it as a valid key would advertise a forgery that does not work")

	assert.Equal(t, "`fragments.<name>`", sites["bundles.BundleFragment"].where)
	assert.Contains(t, sites["bundles.BundleFragment"].keys, "content")
}
