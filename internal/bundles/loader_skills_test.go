package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Loader-side skill resolution tests (Part B3-seam)
// =============================================================================
//
// SkillsFromBundleRef is the loader-side counterpart of CommandsFromBundleRef:
// it resolves a bundle's `skills:` entries into fully-hydrated LoadedSkill
// values (frontmatter + every file's bytes/mode), gated through the same
// per-item trust choke. Tests assert actual resolved payload — name,
// description, file bytes, and mode — never just "no error".

// writeSkillBundle builds a directory-form bundle at
// <bundlesDir>/<bundleName>/bundle.yaml with one skill entry (llmEnabled
// controls its claude-code enablement), plus the skill's SKILL.md/scripts/
// assets tree via writeSkillFixture. Returns the fixture's exact file bytes.
func writeSkillBundle(t *testing.T, fsys afero.Fs, bundlesDir, bundleName, skillName string, llmEnabled bool) map[string][]byte {
	t.Helper()
	bundleDir := bundlesDir + "/" + bundleName
	enabledYAML := "true"
	if !llmEnabled {
		enabledYAML = "false"
	}
	bundleYAML := "name: " + bundleName + "\nversion: \"1.0\"\nskills:\n  " + skillName + ":\n    llm:\n      claude-code:\n        enabled: " + enabledYAML + "\n"
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(bundleYAML), 0644))
	return writeSkillFixture(t, fsys, bundleDir+"/skills/"+skillName, skillName)
}

// TestSkillsFromBundleRef_ResolvesFrontmatterAndFiles proves the loader
// hydrates a bundle's skill entry into its full runtime payload: the parsed
// frontmatter (name/description), every file's exact bytes, and the exec bit
// on scripts/run.sh preserved as a POSIX mode.
func TestSkillsFromBundleRef_ResolvesFrontmatterAndFiles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	files := writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	got := loader.SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1, "one skill resolved from the bundle")

	ls := got[0]
	assert.Equal(t, "skill-bundle/humanize", ls.Name)
	assert.Equal(t, "skill-bundle", ls.Bundle)
	assert.Equal(t, "humanize", ls.Item)
	assert.Equal(t, "humanize", ls.Frontmatter.Name)
	assert.Equal(t, "Does a thing well.", ls.Frontmatter.Description)
	assert.True(t, ls.LLM.ClaudeCode.IsEnabled())

	byPath := make(map[string]LoadedSkillFile, len(ls.Files))
	for _, f := range ls.Files {
		byPath[f.RelPath] = f
	}
	require.Contains(t, byPath, "SKILL.md")
	assert.Equal(t, files["SKILL.md"], byPath["SKILL.md"].Content)
	require.Contains(t, byPath, "scripts/run.sh")
	assert.Equal(t, files["scripts/run.sh"], byPath["scripts/run.sh"].Content)
	assert.Equal(t, uint32(0755), byPath["scripts/run.sh"].Mode, "the exec bit survives loader resolution")
	require.Contains(t, byPath, "assets/logo.png")
	assert.Equal(t, uint32(0644), byPath["assets/logo.png"].Mode)
}

// TestSkillsFromBundleRef_PerEngineDisabledStillResolves proves a skill
// disabled for claude-code still RESOLVES from the loader (enablement
// filtering is the per-engine export mapper's job, backends.claudeSkillExports
// — the loader always returns the full per-engine LLM struct so every engine's
// mapper can make its own decision).
func TestSkillsFromBundleRef_PerEngineDisabledStillResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", false)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	got := loader.SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)
	assert.False(t, got[0].LLM.ClaudeCode.IsEnabled(), "the bundle-authored disablement is carried through")
}

// TestSkillsFromBundleRef_TamperedManifestWithheld proves a skill whose
// bundle.yaml-authored manifest (the `files:` map, what `ctxloom skill sync`/
// sign records) no longer matches the on-disk tree is withheld LOUDLY (nil,
// not a partial/tampered LoadedSkill) — the manifest-drift/tamper guard
// (VerifyExtractedManifest) applies to the tree-sourced path too, not just
// archive extraction.
func TestSkillsFromBundleRef_TamperedManifestWithheld(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	bundleDir := bundlesDir + "/skill-bundle"
	writeSkillFixture(t, fsys, bundleDir+"/skills/humanize", "humanize")

	// Author a bundle.yaml whose recorded manifest hash for SKILL.md does NOT
	// match what's actually on disk — simulating a script edited after signing.
	bundleYAML := `name: skill-bundle
version: "1.0"
skills:
  humanize:
    files:
      SKILL.md: {sha256: "sha256:0000000000000000000000000000000000000000000000000000000000000000", mode: "0644"}
`
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(bundleYAML), 0644))

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	got := loader.SkillsFromBundleRef("skill-bundle")
	assert.Empty(t, got, "a skill whose on-disk tree doesn't match its authored manifest must be withheld, not partially exposed")
}

// TestSkillsFromBundleRef_UnknownBundleReturnsNil proves a bundle ref that
// doesn't resolve returns nil rather than erroring — mirrors
// CommandsFromBundleRef's contract so callers can range over the result
// unconditionally.
func TestSkillsFromBundleRef_UnknownBundleReturnsNil(t *testing.T) {
	loader := NewLoader([]string{"/nowhere"}, false, WithFS(afero.NewMemMapFs()))
	assert.Nil(t, loader.SkillsFromBundleRef("does-not-exist"))
}
