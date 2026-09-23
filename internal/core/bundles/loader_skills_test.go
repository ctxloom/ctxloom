package bundles

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
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

// writeSkillBundle builds the tree <bundlesDir>/v2/<bundleName>/ holding one
// skill (llmEnabled controls its claude-code enablement), with the skill's
// SKILL.md/scripts/assets package via writeSkillFixture. Returns the fixture's
// exact file bytes.
func writeSkillBundle(t *testing.T, fsys afero.Fs, bundlesDir, bundleName, skillName string, llmEnabled bool) map[string][]byte {
	t.Helper()
	root := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	files := writeSkillFixture(t, fsys, filepath.Join(root, bundleName, "skills", skillName), skillName)
	enabledYAML := "true"
	if !llmEnabled {
		enabledYAML = "false"
	}
	writeTree(t, fsys, root, bundleName, "version: \"1.0\"\nskills:\n  "+skillName+":\n    exports:\n      claude-code:\n        enabled: "+enabledYAML+"\n")
	return files
}

// TestSkillsFromBundleRef_ResolvesFrontmatterAndFiles proves the loader
// hydrates a bundle's skill entry into its full runtime payload: the parsed
// frontmatter (name/description), every file's exact bytes, and the exec bit
// on scripts/run.sh preserved as a POSIX mode.
func TestSkillsFromBundleRef_ResolvesFrontmatterAndFiles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	files := writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir})).WithReporter(ledger())
	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1, "one skill resolved from the bundle")

	ls := got[0]
	assert.Equal(t, "skill-bundle/humanize", ls.Name)
	assert.Equal(t, "skill-bundle", ls.Bundle)
	assert.Equal(t, "humanize", ls.Item)
	assert.Equal(t, "humanize", ls.Frontmatter.Name)
	assert.Equal(t, "Does a thing well.", ls.Frontmatter.Description)
	assert.JSONEq(t, `{"enabled":true}`, string(ls.Exports["claude-code"]), "the authored block is carried through, opaque")

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

// TestSkillsFromBundleRef_VendorInvalidFrontmatterStillResolves proves the
// catalog carries a skill whose frontmatter one engine will refuse (here a
// name with a reserved word, which claude's writer rejects at emit): the
// loader has no vendor rules, so the skill resolves with its frontmatter
// verbatim and the refusal, if any, is the emitting engine's to make.
func TestSkillsFromBundleRef_VendorInvalidFrontmatterStillResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "claude-helper", true)

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1, "a vendor-invalid skill still resolves from the bundle")
	assert.Equal(t, "claude-helper", got[0].Item)
	assert.Equal(t, "claude-helper", got[0].Frontmatter.Name, "frontmatter travels verbatim")
}

// TestSkillsFromBundleRef_PerEngineDisabledStillResolves proves a skill
// disabled for claude-code still RESOLVES from the loader: enablement is
// decided by the engine that decodes its own block, so the loader carries
// every block and withholds none.
func TestSkillsFromBundleRef_PerEngineDisabledStillResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", false)

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)
	assert.JSONEq(t, `{"enabled":false}`, string(got[0].Exports["claude-code"]), "the bundle-authored block is carried through")
}

// TestSkillsFromBundleRef_UnknownBundleReturnsNil proves a bundle ref that
// doesn't resolve returns nil rather than erroring — mirrors
// CommandsFromBundleRef's contract so callers can range over the result
// unconditionally.
func TestSkillsFromBundleRef_UnknownBundleReturnsNil(t *testing.T) {
	loader := NewLoader(NewProjectReader(afero.NewMemMapFs(), []string{"/nowhere"}))
	assert.Nil(t, ungated(loader, false).SkillsFromBundleRef("does-not-exist"))
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

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
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
	loader := NewLoader(NewProjectReader(afero.NewMemMapFs(), []string{"/nowhere"}))
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
	root := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	bundleDir := filepath.Join(root, "bundle-a")
	writeSkillFixture(t, fsys, bundleDir+"/skills/aaa-blocked", "aaa-blocked")
	writeSkillFixture(t, fsys, bundleDir+"/skills/zzz-trusted", "zzz-trusted")
	writeTree(t, fsys, root, "bundle-a", "version: \"1.0\"\n")

	pipe := gatedPipe(NewLoader(NewProjectReader(fsys, []string{bundlesDir})),
		blockingGate(nil, "bundle-a#skills/aaa-blocked"), false)

	infos, err := pipe.ListAllSkills()
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

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	ls, err := ungated(loader, false).GetSkill("skill-bundle#skills/humanize")
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

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	_, err := ungated(loader, false).GetSkill("skill-bundle#skills/does-not-exist")
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

	pipe := gatedPipe(NewLoader(NewProjectReader(fsys, []string{bundlesDir})),
		blockingGate(nil, "#skills/humanize"), false)

	_, err := pipe.GetSkill("skill-bundle#skills/humanize")
	require.True(t, errors.Is(err, errs.ErrSkillWithheld), "got %v, want ErrSkillWithheld", err)
}

// TestGetSkill_BareNameSearchesAllBundles proves the bare-name form routes
// through searchSkill and resolves the same payload as the ref form.
func TestGetSkill_BareNameSearchesAllBundles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	ls, err := ungated(loader, false).GetSkill("humanize")
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

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	_, err := ungated(loader, false).GetSkill("does-not-exist-anywhere")
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

	pipe := gatedPipe(NewLoader(NewProjectReader(fsys, []string{bundlesDir})),
		blockingGate(nil, "bundle-blocked#skills/humanize"), false)

	ls, err := pipe.GetSkill("humanize")
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

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	ls, err := ungated(loader, false).GetSkill("humanize")
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

	pipe := gatedPipe(NewLoader(NewProjectReader(fsys, []string{bundlesDir})),
		blockingGate(nil, "#skills/humanize"), false)

	_, err := pipe.GetSkill("humanize")
	require.True(t, errors.Is(err, errs.ErrSkillWithheld), "got %v, want ErrSkillWithheld", err)
}

// =============================================================================
// Refutation pins
// =============================================================================

// TestSkillContent_ExecBitSurvivesLoad: the mode the loader hands the
// materializer comes from the real tree, so a 0755 script stays 0755.
func TestSkillContent_ExecBitSurvivesLoad(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)

	modes := map[string]uint32{}
	for _, f := range got[0].Files {
		modes[f.RelPath] = f.Mode
	}
	assert.Equal(t, uint32(0755), modes["scripts/run.sh"], "the exec bit is load-bearing")
	assert.Equal(t, uint32(0644), modes["SKILL.md"])
}

// TestSkillContent_ManifestResolutionFailureWarns refutes the claim that
// the preimage path was the one withhold in skillContent with no
// clidiag.Warn. The preimage is derived by parsing the package, which CAN
// fail for real (an unparseable SKILL.md) and warns when it does.
//
// This drives the reachable failure and asserts the warning payload, so
// deleting the warn puts the row's own complaint back and this test goes red.
func TestSkillContent_ManifestResolutionFailureWarns(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	root := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	// A package whose SKILL.md has no frontmatter: the preimage must be
	// derived from a package that does not parse.
	require.NoError(t, afero.WriteFile(fsys, filepath.Join(root, "skill-bundle", "skills", "ghost", "SKILL.md"),
		[]byte("no frontmatter here\n"), 0644))
	writeTree(t, fsys, root, "skill-bundle", "version: \"1.0\"\n")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir})).WithReporter(ledger())
	assert.Empty(t, ungated(loader, false).SkillsFromBundleRef("skill-bundle"), "an underivable preimage must withhold")

	out := sink.String()
	assert.Contains(t, out, "ghost", "the withheld skill must be named")
	assert.NotEmpty(t, out, "no withhold in skillContent may be silent")
}

// TestLoadFile_ConcurrencyContract pins what LoadFile's corrected doc now
// asserts, in the two halves the old one-liner ("safe for concurrent use")
// blurred together.
//
// Half one: the CALL is safe. Concurrent LoadFile of the same and different
// paths is race-clean — this runs under -race, so a lock regression around the
// cache fails here rather than intermittently in some caller.
//
// Half two: the RESULT is SHARED, not copied. Identical pointers are exactly
// why the read-only rule is load-bearing; if LoadFile ever started returning
// copies, the caution in its doc would become misleading in the other
// direction, and this is what notices.
func TestLoadFile_ConcurrencyContract(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	for _, n := range []string{"a", "b", "c"} {
		writeTree(t, fsys, bundlesRootIn(bundlesDir, ""), n, "version: \"1.0\"\nfragments:\n  f:\n    content: "+n+"\n")
	}
	l := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))

	var wg sync.WaitGroup
	results := make([]*Bundle, 24)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := []string{"a", "b", "c"}[i%3]
			b, err := l.Load(name)
			assert.NoError(t, err)
			results[i] = b
		}(i)
	}
	wg.Wait()

	for i := range results {
		require.NotNil(t, results[i])
		assert.Same(t, results[i%3], results[i],
			"every caller of the same path shares one *Bundle — the reason it must be treated as read-only")
	}
	assert.NotSame(t, results[0], results[1], "different bundles stay distinct")
}

// TestSkillContent_UmaskCheckoutIsDeliveredNotWithheld is the release-blocking
// case at the level `ctxloom skill list` actually exercises: a skill tree that
// a fresh clone under umask 002 left at 0664/0775.
//
// There is nothing the author could do about those modes: `chmod 0644` +
// `git add` stages nothing, so the repository can never carry a tree at
// 0644. The skill must simply be delivered — and delivered with its exec bit
// intact, which is the part of the mode that does mean something.
func TestSkillContent_UmaskCheckoutIsDeliveredNotWithheld(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)
	skillDir := filepath.Join(paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2), "skill-bundle", "skills", "humanize")

	// The TREE, as a clone under umask 002 leaves it.
	for rel, mode := range map[string]os.FileMode{
		"SKILL.md": 0o664, "scripts/run.sh": 0o775, "assets/logo.png": 0o664,
	} {
		require.NoError(t, fsys.Chmod(skillDir+"/"+rel, mode))
	}

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")

	require.Len(t, got, 1, "a umask-shaped checkout must be delivered: its contents verify and its exec bits agree")
	assert.Empty(t, sink.String(), "and delivered with no warning at all — there is nothing for the author to fix")

	modes := map[string]uint32{}
	for _, f := range got[0].Files {
		modes[f.RelPath] = f.Mode
	}
	assert.NotZero(t, modes["scripts/run.sh"]&0o111, "the exec bit is the part of the mode that survives")
	assert.Zero(t, modes["SKILL.md"]&0o111, "and a plain file must not gain one")
}
