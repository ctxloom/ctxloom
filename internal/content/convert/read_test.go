package convert

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
)

// writeEnvelope puts a bundle.yaml at the tree root. Convert does not write one
// (it plans ITEMS), so every read test that needs an envelope stages it here —
// which is also how a published tree carries its version and description.
func writeEnvelope(t *testing.T, fsys afero.Fs, bundle, yaml string) {
	t.Helper()
	require.NoError(t, afero.WriteFile(fsys, "/tree/"+bundle+"/bundle.yaml", []byte(yaml), 0o644))
}

// everyKindBundle is the document both directions are exercised against: one
// artifact of every kind a bundle can hold, with two hooks whose ALPHABETICAL
// and DECLARED order disagree.
func everyKindBundle() *bundles.Bundle {
	return &bundles.Bundle{
		Name:        "vault",
		Version:     "1.2.3",
		Description: "the vault bundle",
		Fragments: map[string]bundles.BundleFragment{
			"house-style": {Content: "FRAG-BODY", Notes: "N", Tags: []string{"style"}},
		},
		Commands: map[string]bundles.BundleCommand{
			"ship-it": {Content: "CMD-BODY", Description: "ship it"},
		},
		MCP: map[string]bundles.BundleMCP{
			"ledger": {Command: "/bin/ledger", Args: []string{"--serve"}, Env: map[string]string{"MODE": "ro"}},
		},
		Hooks: bundles.BundleHooks{
			PostFileEdit: []bundles.BundleHook{
				{Type: "command", Command: "echo stamp"},
				{Type: "command", Command: "echo audit"},
			},
			SessionStart: []bundles.BundleHook{
				{Type: "command", Command: "echo greet"},
			},
		},
		Skills: map[string]bundles.BundleSkill{"reviewer": {Notes: "review notes"}},
		Profiles: map[string]bundles.BundleProfile{
			"studio": {Description: "STUDIO"},
		},
	}
}

func everyKindOptions() Options {
	return Options{SkillFiles: func(string) ([]content.SkillFile, error) {
		return []content.SkillFile{
			{Path: "SKILL.md", Bytes: []byte("SKILL-BODY"), Mode: content.ModeRegular},
			{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\n"), Mode: content.ModeExecutable},
		}, nil
	}}
}

// stageTree converts a document into a fresh tree, writes the envelope, and
// returns the tree-form bundle ready to read back.
func stageTree(t *testing.T, ctx context.Context, b *bundles.Bundle, envelope string) content.Bundle {
	t.Helper()
	st, fsys := newStore(t)
	require.NoError(t, Convert(ctx, st, "vault", b, everyKindOptions()))
	writeEnvelope(t, fsys, "vault", envelope)
	tree, err := st.Open(ctx, "vault")
	require.NoError(t, err)
	return tree
}

// The inverse must return the SAME document, not a plausible one. Every kind is
// asserted because a converter that dropped one would still produce a bundle
// that loads, assembles and materializes — silently short one surface.
func TestReadTree_ReturnsEveryKindTheTreeHolds(t *testing.T) {
	ctx := context.Background()
	tree := stageTree(t, ctx, everyKindBundle(), "version: 1.2.3\ndescription: the vault bundle\n")

	got, err := bundles.ReadTree(ctx, tree)
	require.NoError(t, err)

	assert.Equal(t, "1.2.3", got.Version, "the envelope's version must survive")
	assert.Equal(t, "the vault bundle", got.Description)

	require.Contains(t, got.Fragments, "house-style")
	assert.Equal(t, "FRAG-BODY", got.Fragments["house-style"].Content)
	assert.Equal(t, []string{"style"}, got.Fragments["house-style"].Tags)
	assert.Equal(t, "N", got.Fragments["house-style"].Notes)

	require.Contains(t, got.Commands, "ship-it")
	assert.Equal(t, "CMD-BODY", got.Commands["ship-it"].Content)
	assert.Equal(t, "ship it", got.Commands["ship-it"].Description)

	require.Contains(t, got.MCP, "ledger")
	assert.Equal(t, "/bin/ledger", got.MCP["ledger"].Command)
	assert.Equal(t, []string{"--serve"}, got.MCP["ledger"].Args)
	assert.Equal(t, map[string]string{"MODE": "ro"}, got.MCP["ledger"].Env)

	require.Contains(t, got.Skills, "reviewer")
	assert.Equal(t, "review notes", got.Skills["reviewer"].Notes)

	require.Contains(t, got.Profiles, "studio")
	assert.Equal(t, "STUDIO", got.Profiles["studio"].Description)

	require.Len(t, got.Hooks.PostFileEdit, 2)
	require.Len(t, got.Hooks.SessionStart, 1)
	assert.Equal(t, "echo greet", got.Hooks.SessionStart[0].Command)
}

// The whole reason the tree format carries an order FIELD. A directory walk
// yields "echo-audit" before "echo-stamp"; declared order is the reverse. A
// reader that trusted the walk would produce a byte-identical-looking bundle
// whose hooks fire in the wrong sequence, and nothing downstream could tell.
func TestReadTree_HooksComeBackInDeclaredOrderNotAlphabeticalOrder(t *testing.T) {
	ctx := context.Background()
	tree := stageTree(t, ctx, everyKindBundle(), "version: 1.2.3\n")

	got, err := bundles.ReadTree(ctx, tree)
	require.NoError(t, err)

	require.Len(t, got.Hooks.PostFileEdit, 2)
	assert.Equal(t, "echo stamp", got.Hooks.PostFileEdit[0].Command,
		"the FIRST-DECLARED hook must come back first, not the alphabetically-first one")
	assert.Equal(t, "echo audit", got.Hooks.PostFileEdit[1].Command)
}

// A skill is the only multi-file item and the only one where a POSIX mode is
// load-bearing. The manifest a read produces is what the loader later verifies
// extracted files against, so a wrong hash or a dropped exec bit is a skill that
// materializes and then does not run.
//
// The expected values come from bundles' OWN renderer rather than being spelled
// out here. Spelling them out is what let this pass while producing a manifest
// the verifier rejected: bare hex where it wanted a "sha256:" prefix. A test
// that restates a format instead of deriving it from the code that consumes it
// pins the implementation, not the contract.
func TestReadTree_SkillManifestCarriesEveryFileWithItsHashAndMode(t *testing.T) {
	ctx := context.Background()
	tree := stageTree(t, ctx, everyKindBundle(), "version: 1.2.3\n")

	got, err := bundles.ReadTree(ctx, tree)
	require.NoError(t, err)

	skill := got.Skills["reviewer"]
	require.Len(t, skill.Files, 2, "every package file must appear in the manifest")

	want := bundles.SkillManifestEntryFor("SKILL.md", []byte("SKILL-BODY"), 0o644)
	assert.Equal(t, want.SHA256, skill.Files["SKILL.md"].SHA256,
		"the hash must be exactly what bundles.VerifyExtractedManifest recomputes")
	assert.Equal(t, want.Mode, skill.Files["SKILL.md"].Mode)
	assert.Equal(t, "0755", skill.Files["scripts/run.sh"].Mode,
		"the declared exec bit must reach the manifest the extractor checks")
}

// A tree with no envelope has no version, no description and no author, and
// nothing else in the tree can supply them. Reading it as a bundle would produce
// one that claims a version it does not have.
func TestReadTree_RefusesATreeWithNoEnvelope(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	require.NoError(t, Convert(ctx, st, "vault", everyKindBundle(), everyKindOptions()))
	tree, err := st.Open(ctx, "vault")
	require.NoError(t, err)

	_, err = bundles.ReadTree(ctx, tree)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle.yaml", "the error must name the file that is missing")
}

// A half-migrated tree — items in files AND items still inline in the envelope —
// has two answers for the same item and no rule for which wins. Silently
// preferring one is how a stale inline copy keeps being served after the file
// was edited.
func TestReadTree_RefusesAnEnvelopeThatAlsoDeclaresInlineItems(t *testing.T) {
	ctx := context.Background()
	tree := stageTree(t, ctx, everyKindBundle(),
		"version: 1.2.3\nfragments:\n  house-style:\n    content: STALE-INLINE-COPY\n")

	_, err := bundles.ReadTree(ctx, tree)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fragments",
		"the error must name which inline key made the tree ambiguous")
}

// Read must not invent a bundle out of an empty directory: an empty bundle
// loads, assembles and materializes perfectly happily while delivering nothing,
// which is this codebase's characteristic silent failure.
func TestReadTree_RefusesATreeThatHoldsNoItemsAtAll(t *testing.T) {
	ctx := context.Background()
	st, fsys := newStore(t)
	require.NoError(t, fsys.MkdirAll("/tree/vault", 0o755))
	writeEnvelope(t, fsys, "vault", "version: 1.2.3\n")
	tree, err := st.Open(ctx, "vault")
	require.NoError(t, err)

	_, err = bundles.ReadTree(ctx, tree)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no items")
}
