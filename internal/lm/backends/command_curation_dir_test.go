// Directory-profile prompt-curation tests verify that a directory profile
// (.ctxloom/profiles/<name>.yaml) carrying a commands: list gets the SAME opt-in
// curation as an inline profile (prompt_curation_test.go): a non-empty list
// exports EXACTLY those (force-enabled, version-pinned, gated), an uncurated
// profile falls back to its referenced bundles' commands (profile-scoped, not
// the old global sweep), the set unions across parent inheritance, and a
// directory default unions with an inline default. The directory path
// reaches LoadCommandExports' curation point through profiles.ResolvedProfile
// (the loader fallback in resolveProfilePromptRefs), not config's inline map.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

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
// LoadCommandExports.
//
// Built through config.Load (a written config.yaml + WithAppDir), not a
// struct literal: config.Config's fields are unexported outside the config
// package (v0.7.0-pre1 config-manager rework) precisely so a caller can't
// synthesize a Config that skips the loader's schema/upgrade/default-merge
// pipeline — this test-only builder now goes through the same public path
// every real caller does.
func dirCurationCfg(t *testing.T, defaults []string, dirProfiles map[string]string, inline map[string]config.Profile) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	for name, body := range dirProfiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
	}

	doc := map[string]any{
		"default_agent": "default",
		"agents": map[string]any{
			"default": map[string]any{"profiles": defaults},
		},
	}
	if len(inline) > 0 {
		doc["profiles"] = map[string]any{"definitions": inline}
	}
	data, err := yaml.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), data, 0o644))

	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	// Setting AppPaths arms companion probing, which execs the companion
	// binaries on the HOST's PATH — so these exact-set assertions would pick up
	// e.g. ltk's commands on a machine that has ltk installed and pass on one that
	// doesn't. The fixture, not the machine, decides what is exported here.
	cfg.DisableCompanionProbe()
	return cfg
}

// TestLoadCommandExports_DirProfileCuratedSetExportsExactlyThose proves a
// directory profile's non-empty commands: list exports ONLY the listed prompts,
// suppressing the global auto-export for the others — parity with the inline
// CuratedSetExportsExactlyThose case.
func TestLoadCommandExports_DirProfileCuratedSetExportsExactlyThose(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "commands:\n  - \"dev-tools#commands/review\"\n",
	}, nil)

	prompts := LoadCommandExports(cfg, nil, seedOption(t, devToolsSeed()))

	assert.ElementsMatch(t, []string{"review"}, bundlePromptItems(prompts),
		"only the directory profile's listed prompt is exported; the globally-flagged 'hidden' is suppressed")
}

// TestLoadCommandExports_DirProfileUncuratedScopesToReferencedBundles proves an
// uncurated directory profile falls back to the commands of the bundles IT
// references — profile-scoped, not the old global sweep. The profile pulls
// dev-tools, so dev-tools' commands export; a second seeded bundle the profile
// does NOT reference contributes nothing.
func TestLoadCommandExports_DirProfileUncuratedScopesToReferencedBundles(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "bundles:\n  - dev-tools\n", // references dev-tools only, no commands:
	}, nil)

	seed := map[string]*bundles.Bundle{
		"dev-tools": {Commands: map[string]bundles.BundleCommand{
			"review":  {Content: "REVIEW"},
			"explain": {Content: "EXPLAIN"},
		}},
		"other-tools": {Commands: map[string]bundles.BundleCommand{
			"unrelated": {Content: "UNRELATED"},
		}},
	}

	prompts := LoadCommandExports(cfg, nil, seedOption(t, seed))

	items := bundlePromptItems(prompts)
	assert.ElementsMatch(t, []string{"review", "explain"}, items,
		"an uncurated profile exports the commands of the bundles it references")
	assert.NotContains(t, items, "unrelated",
		"a bundle the profile does not reference contributes no commands (scoped, not global)")
}

// TestLoadCommandExports_DirProfileCurationUnionsParents proves a directory
// profile's curated set unions across parent inheritance, exercising the Commands
// threading through profiles.resolveProfileRecursive + ResolvedProfile.Merge.
func TestLoadCommandExports_DirProfileCurationUnionsParents(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"child"}, map[string]string{
		"base":  "commands:\n  - \"dev-tools#commands/review\"\n",
		"child": "parents:\n  - base\ncommands:\n  - \"dev-tools#commands/explain\"\n",
	}, nil)

	prompts := LoadCommandExports(cfg, nil, seedOption(t, devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "explain"}, bundlePromptItems(prompts),
		"curated set unions the directory parent (review) + child (explain); 'hidden'/'commit' stay suppressed")
}

// TestLoadCommandExports_DirProfileMergesWithInlineDefault proves the curated set
// unions across a DIRECTORY default profile and an INLINE default profile — the
// two resolution paths fold into one export set, the headline directory-curation
// gap this change closes.
func TestLoadCommandExports_DirProfileMergesWithInlineDefault(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"inlineP", "dirP"},
		map[string]string{
			"dirP": "commands:\n  - \"dev-tools#commands/commit\"\n",
		},
		map[string]config.Profile{
			"inlineP": {Commands: []string{"dev-tools#commands/review"}},
		})

	prompts := LoadCommandExports(cfg, nil, seedOption(t, devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "commit"}, bundlePromptItems(prompts),
		"curated set unions the inline default (review) with the directory default (commit)")
}

// TestLoadCommandExports_DirProfileCuratedGated proves a directory-curated prompt
// is trust-gated exactly like an inline one: granting its content hash exports
// it, denying withholds it (fail-closed; only builtins remain). The gate is the
// same per-item executable gate the inline path uses.
//
// NOTE: unlike the inline CuratedVersionPinnedAndGated test, this exercises the
// UN-pinned ref. A directory profile requires AppPaths, which always wires the
// production bundleVersionResolver (config.SeededBundleLoader, last-wins over any
// injected resolver), so a fake "@<commit>" resolver can't be substituted here —
// successful version resolution is shared loadCuratedPrompts code already covered
// by TestLoadCommandExports_CuratedVersionPinnedAndGated. The pin ROUTING through
// the directory path is proven by DirProfileCuratedPinRoutedAndFailClosed below.
func TestLoadCommandExports_DirProfileCuratedGated(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "commands:\n  - \"dev-tools#commands/review\"\n",
	}, nil)
	seed := seedOption(t, devToolsSeed())

	// Gate granting exactly the review prompt's content hash → exported.
	want := promptRawHash("REVIEW")
	cfg.SetExecutableTrustGate(hashAuthorizer(want))
	prompts := LoadCommandExports(cfg, nil, seed)
	require.Equal(t, []string{"review"}, bundlePromptItems(prompts),
		"a granted directory-curated prompt is exported")

	// Gate denying → withheld (fail-closed); only builtins remain.
	cfg.SetExecutableTrustGate(testAuthorizer(false))
	denied := LoadCommandExports(cfg, nil, seed)
	assert.Empty(t, bundlePromptItems(denied),
		"an un-granted directory-curated prompt must be withheld")
}

// TestLoadCommandExports_DirProfileCuratedPinRoutedAndFailClosed proves the
// directory path HONORS an "@<commit>" pin: the ref is split (version-agnostic
// identity + version) and routed to the version-aware load, NOT silently
// downgraded to the unpinned HEAD. The seed carries an (unpinned) "review", so a
// path that ignored "@c1" would export it; instead the pin routes to
// GetPromptAtVersion, which the local production resolver can't serve for a fake
// commit, so it is warned-and-skipped (fault-tolerant fail-closed): a
// version fetch failure means withheld, on the directory path too.
func TestLoadCommandExports_DirProfileCuratedPinRoutedAndFailClosed(t *testing.T) {
	cfg := dirCurationCfg(t, []string{"x"}, map[string]string{
		"x": "commands:\n  - \"dev-tools#commands/review@c1\"\n",
	}, nil)

	prompts := LoadCommandExports(cfg, nil, seedOption(t, devToolsSeed()))

	assert.Empty(t, bundlePromptItems(prompts),
		"a curated @<commit> pin that can't be resolved is fail-closed, not silently downgraded to the unpinned HEAD")
}
