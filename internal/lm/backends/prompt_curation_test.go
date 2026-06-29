// Profile prompt-curation tests verify the opt-in exposure semantics of
// LoadPromptExports: a profile with a non-empty prompts: list exports EXACTLY
// those (force-enabled, version-pinned, gated), suppressing the global
// flag-based auto-export; an empty list keeps today's global behavior; the
// curated set unions across parents + multiple default profiles.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// curationProfiles builds a config whose default profiles are the given inline
// definitions, with HOME isolated. AppPaths is left empty so the only bundle
// source is the seed/resolver passed to LoadPromptExports (no production version
// resolver is wired, so a test resolver survives).
func curationCfg(t *testing.T, defaults []string, defs map[string]config.Profile) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return &config.Config{
		Profiles: config.ProfilesConfig{Defaults: defaults, Definitions: defs},
	}
}

// optOut returns an LLMExports with every backend's slash-command export
// explicitly disabled (the global opt-out), to prove curation force-enables.
func optOut() bundles.LLMExports {
	off := false
	var x bundles.LLMExports
	x.ClaudeCode.Enabled = &off
	x.Antigravity.Enabled = &off
	x.Codex.Enabled = &off
	return x
}

// bundlePromptItems returns the bare item names of the bundle (non-builtin)
// prompts in an export set. Builtins carry no Item, so they are filtered out and
// the assertion is about the curated/auto-exported bundle prompt set only.
func bundlePromptItems(prompts []*bundles.LoadedContent) []string {
	var items []string
	for _, p := range prompts {
		if p.Item != "" {
			items = append(items, p.Item)
		}
	}
	return items
}

func devToolsSeed() map[string]*bundles.Bundle {
	return map[string]*bundles.Bundle{
		"dev-tools": {Prompts: map[string]bundles.BundlePrompt{
			"review":  {Content: "REVIEW"},
			"explain": {Content: "EXPLAIN"},
			"commit":  {Content: "COMMIT"},
			"hidden":  {Content: "HIDDEN"},
		}},
	}
}

// TestLoadPromptExports_CuratedSetExportsExactlyThose proves a non-empty
// prompts: list exports ONLY the listed prompts; a globally-flagged prompt NOT
// in the list is suppressed for that profile.
func TestLoadPromptExports_CuratedSetExportsExactlyThose(t *testing.T) {
	cfg := curationCfg(t, []string{"p"}, map[string]config.Profile{
		"p": {Prompts: []string{"dev-tools#prompts/review"}},
	})

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review"}, bundlePromptItems(prompts),
		"only the profile-listed prompt is exported; the globally-flagged 'hidden' is suppressed")
}

// TestLoadPromptExports_EmptyListKeepsGlobalExport proves a profile with NO
// prompts: list keeps today's global flag-based auto-export (opt-in: no silent
// change).
func TestLoadPromptExports_EmptyListKeepsGlobalExport(t *testing.T) {
	cfg := curationCfg(t, []string{"p"}, map[string]config.Profile{
		"p": {}, // no prompts: list
	})

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "explain", "commit", "hidden"}, bundlePromptItems(prompts),
		"with no curation, every bundle prompt is auto-exported (global behavior)")
}

// TestLoadPromptExports_CurationUnionsParentsAndDefaults proves the curated set
// is the union across parent inheritance and multiple default profiles, matching
// the Fragments merge rules.
func TestLoadPromptExports_CurationUnionsParentsAndDefaults(t *testing.T) {
	cfg := curationCfg(t, []string{"child", "other"}, map[string]config.Profile{
		"base":  {Prompts: []string{"dev-tools#prompts/review"}},
		"child": {Parents: []string{"base"}, Prompts: []string{"dev-tools#prompts/explain"}},
		"other": {Prompts: []string{"dev-tools#prompts/commit"}},
	})

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(devToolsSeed()))

	assert.ElementsMatch(t, []string{"review", "explain", "commit"}, bundlePromptItems(prompts),
		"curated set unions parent (review) + child (explain) + the other default (commit); 'hidden' stays suppressed")
}

// TestLoadPromptExports_CuratedForceEnablesOptOut proves a profile-listed prompt
// is exported even when its bundle opted it out of the global slash-command
// export — the profile explicitly curates it.
func TestLoadPromptExports_CuratedForceEnablesOptOut(t *testing.T) {
	seed := map[string]*bundles.Bundle{
		"dev-tools": {Prompts: map[string]bundles.BundlePrompt{
			"optout": {Content: "OPTOUT", LLM: optOut()},
		}},
	}
	cfg := curationCfg(t, []string{"p"}, map[string]config.Profile{
		"p": {Prompts: []string{"dev-tools#prompts/optout"}},
	})

	prompts := LoadPromptExports(cfg, bundles.WithSeededBundles(seed))
	require.Equal(t, []string{"optout"}, bundlePromptItems(prompts))

	// The downstream backend mapper must see it ENABLED despite the bundle's
	// opt-out, since the profile curated it.
	ex := commandExportsFor("claude-code", prompts)
	var found bool
	for _, e := range ex {
		if e.Name == "dev-tools/optout" {
			found = true
			assert.True(t, e.Enabled, "a curated opt-out prompt must be force-enabled for export")
		}
	}
	assert.True(t, found, "the curated prompt must reach the backend export set")
}

// promptRawHash is the effective-content hash of a no-distill prompt body
// (preferDistilled true ⇒ raw bytes), the value the gate keys on.
func promptRawHash(body string) string {
	p := bundles.BundlePrompt{Content: body}
	h, _ := p.EffectiveContentHash(true)
	return h
}

// TestLoadPromptExports_CuratedVersionPinnedAndGated proves a curated prompt
// pinned to "@<commit>" exports that historical version, and that the export is
// gated by the pinned version's own content hash (a deny withholds it).
func TestLoadPromptExports_CuratedVersionPinnedAndGated(t *testing.T) {
	resolver := func(_canonical, commit string) (*bundles.Bundle, error) {
		if commit != "c1" {
			t.Fatalf("unexpected commit %q", commit)
		}
		return &bundles.Bundle{Prompts: map[string]bundles.BundlePrompt{"review": {Content: "V1-PINNED"}}}, nil
	}
	cfg := curationCfg(t, []string{"p"}, map[string]config.Profile{
		"p": {Prompts: []string{"dev-tools#prompts/review@c1"}},
	})

	// Gate granting exactly the pinned version's hash → exported as that version.
	want := promptRawHash("V1-PINNED")
	cfg.SetExecutableTrustGate(func(_ref, hash, _form string) bool { return hash == want })
	prompts := LoadPromptExports(cfg, bundles.WithVersionResolver(resolver))
	require.Equal(t, []string{"review"}, bundlePromptItems(prompts))
	for _, p := range prompts {
		if p.Item == "review" {
			assert.Equal(t, "V1-PINNED", p.Content, "the pinned historical version is exported, not the default")
		}
	}

	// Gate denying the pinned version → withheld, so no bundle prompt exports
	// (fail-closed; only builtins remain).
	cfg.SetExecutableTrustGate(func(_ref, _hash, _form string) bool { return false })
	denied := LoadPromptExports(cfg, bundles.WithVersionResolver(resolver))
	assert.Empty(t, bundlePromptItems(denied), "an un-granted pinned curated version must be withheld")
}
