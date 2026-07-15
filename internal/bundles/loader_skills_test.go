package bundles

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
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

// =============================================================================
// ListAllSkills tests (loop over 0/1/many bundles and skills)
// =============================================================================

// TestListAllSkills_ReturnsInfoAcrossBundles proves ListAllSkills' loop over
// every bundle AND every bundle's SkillNames() surfaces each skill's payload
// (description, file count, tags) — not just "no error" — across MULTIPLE
// bundles each with their own skill.
func TestListAllSkills_ReturnsInfoAcrossBundles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "bundle-a", "humanize", true)
	writeSkillBundle(t, fsys, bundlesDir, "bundle-b", "otherskill", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	infos, err := loader.ListAllSkills()
	require.NoError(t, err)
	require.Len(t, infos, 2, "one skill from each of the two bundles")

	byName := make(map[string]SkillInfo, len(infos))
	for _, si := range infos {
		byName[si.Name] = si
	}

	require.Contains(t, byName, "bundle-a/humanize")
	a := byName["bundle-a/humanize"]
	assert.Equal(t, "bundle-a", a.Bundle)
	assert.Equal(t, "Does a thing well.", a.Description)
	assert.Equal(t, 3, a.FileCount, "SKILL.md + scripts/run.sh + assets/logo.png")

	require.Contains(t, byName, "bundle-b/otherskill")
	b := byName["bundle-b/otherskill"]
	assert.Equal(t, "bundle-b", b.Bundle)
	assert.Equal(t, 3, b.FileCount)
}

// TestListAllSkills_NoBundlesReturnsEmpty proves the 0-bundle case of
// ListAllSkills' loop returns an empty, error-free result rather than nil
// dereference or a spurious error.
func TestListAllSkills_NoBundlesReturnsEmpty(t *testing.T) {
	loader := NewLoader([]string{"/nowhere"}, false, WithFS(afero.NewMemMapFs()))
	infos, err := loader.ListAllSkills()
	require.NoError(t, err)
	assert.Empty(t, infos)
}

// TestListAllSkills_WithheldSkillOmittedNotErrored proves a skill the trust
// gate withholds is silently omitted from ListAllSkills (skillContent already
// warns) rather than aborting the whole listing or leaking a partial entry.
//
// Both skills live in the SAME bundle, with the withheld one sorting FIRST
// (bundle.SkillNames() is sorted) — this pins ListAllSkills' inner loop
// CONTINUES past a withheld skill to the next name rather than stopping the
// bundle's scan there: an INVERT_LOOPCTRL mutant turning that `continue` into
// `break` would drop the trusted sibling that sorts after it, which a
// single-skill-per-bundle fixture could never distinguish.
func TestListAllSkills_WithheldSkillOmittedNotErrored(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	bundleDir := bundlesDir + "/bundle-a"
	bundleYAML := `name: bundle-a
version: "1.0"
skills:
  aaa-blocked:
    llm:
      claude-code:
        enabled: true
  zzz-trusted:
    llm:
      claude-code:
        enabled: true
`
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(bundleYAML), 0644))
	writeSkillFixture(t, fsys, bundleDir+"/skills/aaa-blocked", "aaa-blocked")
	writeSkillFixture(t, fsys, bundleDir+"/skills/zzz-trusted", "zzz-trusted")

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys),
		WithTrustGate(blockingGate(nil, "bundle-a#skills/aaa-blocked")))

	infos, err := loader.ListAllSkills()
	require.NoError(t, err)
	require.Len(t, infos, 1, "the withheld skill (sorts first) must be skipped, not stop the scan of its bundle's remaining skills")
	assert.Equal(t, "bundle-a/zzz-trusted", infos[0].Name)
}

// =============================================================================
// GetSkill tests: ref form (skillFromBundle) and bare-name form (searchSkill)
// =============================================================================

// TestGetSkill_RefFormFindsSpecificBundle proves the "bundle#skills/name" ref
// form routes through skillFromBundle and resolves the full payload.
func TestGetSkill_RefFormFindsSpecificBundle(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	ls, err := loader.GetSkill("skill-bundle#skills/humanize")
	require.NoError(t, err)
	assert.Equal(t, "skill-bundle/humanize", ls.Name)
	assert.Equal(t, "Does a thing well.", ls.Frontmatter.Description)
}

// TestGetSkill_RefFormUnknownSkillInBundleErrors proves an unresolvable
// skill name within an otherwise-valid bundle ref errors loudly.
func TestGetSkill_RefFormUnknownSkillInBundleErrors(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	_, err := loader.GetSkill("skill-bundle#skills/does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestGetSkill_RefFormWithheldByTrustGateReturnsErrSkillWithheld proves the
// ref-form path (skillFromBundle) surfaces ErrSkillWithheld — distinct from
// ErrSkillNotFound — when the trust gate withholds a skill that DOES exist in
// the named bundle.
func TestGetSkill_RefFormWithheldByTrustGateReturnsErrSkillWithheld(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys),
		WithTrustGate(blockingGate(nil, "#skills/humanize")))

	_, err := loader.GetSkill("skill-bundle#skills/humanize")
	require.True(t, errors.Is(err, errs.ErrSkillWithheld), "got %v, want ErrSkillWithheld", err)
}

// TestGetSkill_BareNameSearchesAllBundles proves the bare-name form routes
// through searchSkill and resolves the same payload as the ref form.
func TestGetSkill_BareNameSearchesAllBundles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	ls, err := loader.GetSkill("humanize")
	require.NoError(t, err)
	assert.Equal(t, "skill-bundle/humanize", ls.Name)
}

// TestGetSkill_BareNameNotFoundAnywhereReturnsErrSkillNotFound proves a name
// matching no bundle's skill at all returns ErrSkillNotFound (distinct from
// ErrSkillWithheld — nothing was withheld, it simply doesn't exist).
func TestGetSkill_BareNameNotFoundAnywhereReturnsErrSkillNotFound(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	_, err := loader.GetSkill("does-not-exist-anywhere")
	require.True(t, errors.Is(err, errs.ErrSkillNotFound), "got %v, want ErrSkillNotFound", err)
}

// TestSearchSkill_WithheldInOneBundleStillResolvesFromTrustedSibling proves
// searchSkill's documented contract: a gate-withheld match in one bundle does
// NOT end the scan — a trusted copy of the SAME skill name in another bundle
// still wins.
func TestSearchSkill_WithheldInOneBundleStillResolvesFromTrustedSibling(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "bundle-blocked", "humanize", true)
	writeSkillBundle(t, fsys, bundlesDir, "bundle-trusted", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys),
		WithTrustGate(blockingGate(nil, "bundle-blocked#skills/humanize")))

	ls, err := loader.GetSkill("humanize")
	require.NoError(t, err)
	assert.Equal(t, "bundle-trusted/humanize", ls.Name, "a withheld copy in one bundle must not block a trusted copy in another")
}

// TestSearchSkill_SkipsBundleWithoutTheSkillAndContinuesToNextBundle proves
// searchSkill's `!ok` continue (a bundle that simply doesn't carry the named
// skill at all) does not end the scan: "bundle-a" sorts first and does NOT
// have "humanize"; "bundle-b" sorts after it and does. An INVERT_LOOPCTRL
// mutant turning that `continue` into `break` would stop at bundle-a and
// never reach bundle-b's match, turning a resolvable search into
// ErrSkillNotFound.
func TestSearchSkill_SkipsBundleWithoutTheSkillAndContinuesToNextBundle(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "bundle-a", "unrelated", true)
	writeSkillBundle(t, fsys, bundlesDir, "bundle-b", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys))
	ls, err := loader.GetSkill("humanize")
	require.NoError(t, err, "a bundle without the named skill must be skipped, not stop the scan")
	assert.Equal(t, "bundle-b/humanize", ls.Name)
}

// TestSearchSkill_AllWithheldReturnsErrSkillWithheld proves that when EVERY
// bundle's match is withheld, searchSkill reports ErrSkillWithheld — not
// ErrSkillNotFound, since the skill does exist, just not for this trust
// posture.
func TestSearchSkill_AllWithheldReturnsErrSkillWithheld(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "bundle-a", "humanize", true)
	writeSkillBundle(t, fsys, bundlesDir, "bundle-b", "humanize", true)

	loader := NewLoader([]string{bundlesDir}, false, WithFS(fsys),
		WithTrustGate(blockingGate(nil, "#skills/humanize")))

	_, err := loader.GetSkill("humanize")
	require.True(t, errors.Is(err, errs.ErrSkillWithheld), "got %v, want ErrSkillWithheld", err)
}
