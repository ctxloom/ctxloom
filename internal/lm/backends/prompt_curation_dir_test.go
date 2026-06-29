// Directory-profile prompt-curation tests verify that a directory profile
// (.ctxloom/profiles/<name>.yaml) carrying a prompts: list gets the SAME opt-in
// curation as an inline profile (prompt_curation_test.go): a non-empty list
// exports EXACTLY those (force-enabled, version-pinned, gated), an empty list
// keeps today's global auto-export, the set unions across parent inheritance,
// and a directory default unions with an inline default. The directory path
// reaches LoadPromptExports' curation point through profiles.ResolvedProfile
// (the loader fallback in resolveProfilePromptRefs), not config's inline map.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// dirCurationCfg builds a config whose default profiles resolve from real
// .ctxloom/profiles/<name>.yaml files (dirProfiles, name → YAML body) and/or the
// inline config.yaml map (inline). HOME is junked and AppPaths points at the
// tempdir's .ctxloom; the profile loader reads the real filesystem (no fs is
// wired), matching how GetProfileDirs/os.Stat resolve directory profiles in
// production. The only bundle source remains the seed/resolver passed to
// LoadPromptExports.
func dirCurationCfg(t *testing.T, defaults []string, dirProfiles map[string]string, inline map[string]config.Profile) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	for name, body := range dirProfiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
	}
	return &config.Config{
		AppPaths: []string{appDir},
		Profiles: config.ProfilesConfig{Defaults: defaults, Definitions: inline},
	}
}

// TestLoadPromptExports_DirProfileCuratedSetExportsExactlyThose proves a
// directory profile's non-empty prompts: list exports ONLY the listed prompts,
// suppressing the global auto-export for the others — parity with the inline
// CuratedSetExportsExactlyThose case.
func TestLoadPromptExports_DirProfileCuratedSetExportsExactlyThose(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "prompts:\n  - \"dev-tools#prompts/review\"\n",
	}, nil)

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review"}, bundlePromptItems(prompts),
		"only the directory profile's listed prompt is exported; the globally-flagged 'hidden' is suppressed")
}

// TestLoadPromptExports_DirProfileEmptyListKeepsGlobalExport proves a directory
// profile with NO prompts: list keeps today's global flag-based auto-export
// (opt-in: no silent change) — parity with the inline EmptyList case.
func TestLoadPromptExports_DirProfileEmptyListKeepsGlobalExport(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "bundles:\n  - dev-tools\n", // no prompts: list
	}, nil)

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "explain", "commit", "hidden"}, bundlePromptItems(prompts),
		"with no curation, every bundle prompt is auto-exported (global behavior)")
}

// TestLoadPromptExports_DirProfileCurationUnionsParents proves a directory
// profile's curated set unions across parent inheritance, exercising the Prompts
// threading through profiles.resolveProfileRecursive + ResolvedProfile.Merge.
func TestLoadPromptExports_DirProfileCurationUnionsParents(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"child"}, map[string]string{
		"base":  "prompts:\n  - \"dev-tools#prompts/review\"\n",
		"child": "parents:\n  - base\nprompts:\n  - \"dev-tools#prompts/explain\"\n",
	}, nil)

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "explain"}, bundlePromptItems(prompts),
		"curated set unions the directory parent (review) + child (explain); 'hidden'/'commit' stay suppressed")
}

// TestLoadPromptExports_DirProfileMergesWithInlineDefault proves the curated set
// unions across a DIRECTORY default profile and an INLINE default profile — the
// two resolution paths fold into one export set, the headline directory-curation
// gap this change closes.
func TestLoadPromptExports_DirProfileMergesWithInlineDefault(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"inlineP", "dirP"},
		map[string]string{
			"dirP": "prompts:\n  - \"dev-tools#prompts/commit\"\n",
		},
		map[string]config.Profile{
			"inlineP": {Prompts: []string{"dev-tools#prompts/review"}},
		})

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "commit"}, bundlePromptItems(prompts),
		"curated set unions the inline default (review) with the directory default (commit)")
}

// TestLoadPromptExports_DirProfileCuratedGated proves a directory-curated prompt
// is trust-gated exactly like an inline one: granting its content hash exports
// it, denying withholds it (fail-closed; only builtins remain). The gate is the
// same per-item executable gate the inline path uses.
//
// NOTE: unlike the inline CuratedVersionPinnedAndGated test, this exercises the
// UN-pinned ref. A directory profile requires AppPaths, which always wires the
// production bundleVersionResolver (config.SeededBundleLoader, last-wins over any
// injected resolver), so a fake "@<commit>" resolver can't be substituted here —
// successful version resolution is shared loadCuratedPrompts code already covered
// by TestLoadPromptExports_CuratedVersionPinnedAndGated. The pin ROUTING through
// the directory path is proven by DirProfileCuratedPinRoutedAndFailClosed below.
func TestLoadPromptExports_DirProfileCuratedGated(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "prompts:\n  - \"dev-tools#prompts/review\"\n",
	}, nil)
	seed := bundles.WithSeededBundles(devToolsSeed())

	// Gate granting exactly the review prompt's content hash → exported.
	want := promptRawHash("REVIEW")
	cfg.SetExecutableTrustGate(func(_ref, hash, _form string) bool { return hash == want })
	prompts := LoadPromptExports(cfg, seed)
	require.Equal(t, []string{"review"}, bundlePromptItems(prompts),
		"a granted directory-curated prompt is exported")

	// Gate denying → withheld (fail-closed); only builtins remain.
	cfg.SetExecutableTrustGate(func(_ref, _hash, _form string) bool { return false })
	denied := LoadPromptExports(cfg, seed)
	assert.Empty(t, bundlePromptItems(denied),
		"an un-granted directory-curated prompt must be withheld")
}

// TestLoadPromptExports_DirProfileCuratedPinRoutedAndFailClosed proves the
// directory path HONORS an "@<commit>" pin: the ref is split (version-agnostic
// identity + version) and routed to the version-aware load, NOT silently
// downgraded to the unpinned HEAD. The seed carries an (unpinned) "review", so a
// path that ignored "@c1" would export it; instead the pin routes to
// GetPromptAtVersion, which the local production resolver can't serve for a fake
// commit, so it is warned-and-skipped (fault-tolerant fail-closed) — exactly the
// b626431 "version fetch failure → withheld" semantics, on the directory path.
func TestLoadPromptExports_DirProfileCuratedPinRoutedAndFailClosed(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "prompts:\n  - \"dev-tools#prompts/review@c1\"\n",
	}, nil)

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.Empty(t, bundlePromptItems(prompts),
		"a curated @<commit> pin that can't be resolved is fail-closed, not silently downgraded to the unpinned HEAD")
}
