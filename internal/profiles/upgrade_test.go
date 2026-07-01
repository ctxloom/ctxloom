package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// TestPromptSelectorUpgrade_MigratesSelectors pins the prompt→skill selector
// migration: legacy "#prompts/" and ":prompts/" item selectors in a profile's
// bundles/bundle_items are rewritten to the skills section on load.
func TestPromptSelectorUpgrade_MigratesSelectors(t *testing.T) {
	in := []byte("name: p\nbundles:\n  - core#prompts/review\n  - alias/other:prompts/lint\nbundle_items:\n  - b#prompts/x\n")
	out, applied := upgrade.Pipeline{promptSelectorUpgrade{}}.Run(in)
	require.NotEmpty(t, applied, "migration should fire on legacy prompt selectors")
	s := string(out)
	assert.Contains(t, s, "core#skills/review")
	assert.Contains(t, s, "other:skills/lint")
	assert.Contains(t, s, "b#skills/x")
	assert.NotContains(t, s, "prompts/", "no prompt selector should remain")
}

// TestPromptSelectorUpgrade_Idempotent confirms a profile already on the skills
// vocabulary is left untouched.
func TestPromptSelectorUpgrade_Idempotent(t *testing.T) {
	in := []byte("name: p\nbundles:\n  - core#skills/review\n")
	_, applied := upgrade.Pipeline{promptSelectorUpgrade{}}.Run(in)
	assert.Empty(t, applied, "skills-vocabulary profile must not change")
}

// personalURL and defaultURL are the canonical repo URLs the test alias resolver
// maps the two stock remotes to.
const (
	personalURL = "https://github.com/benjaminabbitt/ctxloom-personal"
	defaultURL  = "https://github.com/ctxloom/ctxloom-default"
)

// testAliasToURL is the alias→URL resolver used across the upgrade tests, standing
// in for the registry-backed resolver the loader wires in production.
func testAliasToURL(alias string) string {
	switch alias {
	case "personal":
		return personalURL
	case "ctxloom-default":
		return defaultURL
	}
	return ""
}

// runProfileUpgrade is a helper: run the profile upgrade pipeline for a profile
// whose own remote URL is ownURL over data, and report the upgraded bytes plus
// which upgrades fired.
func runProfileUpgrade(ownURL string, data []byte) ([]byte, []string) {
	return profileUpgrades(ownURL, testAliasToURL).Run(data)
}

// TestBundleRefCanonicalize_ShortRefsBecomeCanonical verifies that bare and
// alias-prefixed bundle refs are rewritten to canonical URL form, that a
// cherry-picked ":fragments/…" selector becomes the canonical "#fragments/…",
// and that a foreign alias resolves against ITS repo, not the profile's own.
func TestBundleRefCanonicalize_ShortRefsBecomeCanonical(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - core-practices\n" +
		"  - personal/developer-mindset\n" +
		"  - ctxloom-default/git\n" +
		"  - go-development:fragments/testing\n")

	out, applied := runProfileUpgrade(personalURL, in)

	require.NotEmpty(t, applied, "upgrade should fire when short refs are present")
	got := string(out)
	// Bare → profile's own remote.
	assert.Contains(t, got, "- "+personalURL+"@bundles/core-practices")
	// Alias matching the profile's own remote → that repo (redundant prefix dropped).
	assert.Contains(t, got, "- "+personalURL+"@bundles/developer-mindset")
	// Foreign alias → ITS repo, not the profile's own.
	assert.Contains(t, got, "- "+defaultURL+"@bundles/git")
	// Cherry-pick: bundle canonicalized, ':' selector normalized to '#'.
	assert.Contains(t, got, "- "+personalURL+"@bundles/go-development#fragments/testing")
	// No short/alias form should survive.
	assert.NotContains(t, got, "- personal/")
	assert.NotContains(t, got, "- ctxloom-default/")
}

// TestBundleRefCanonicalize_CanonicalURLsUntouched is a regression guard: a
// canonical URL ref is already fully qualified and must pass through verbatim.
// The scheme colon in "https://" must NOT be mistaken for the cherry-pick ':'
// separator — doing so split the bundle name down to "https" and produced a
// nonsense "<remote>/https://…" ref that no longer resolved.
func TestBundleRefCanonicalize_CanonicalURLsUntouched(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - " + defaultURL + "@bundles/default\n" +
		"  - " + personalURL + "@bundles/just\n" +
		"  - core-practices\n")

	out, applied := runProfileUpgrade(personalURL, in)

	got := string(out)
	// The bare ref still canonicalizes...
	require.NotEmpty(t, applied)
	assert.Contains(t, got, "- "+personalURL+"@bundles/core-practices")
	// ...but the canonical URLs are left exactly as-is.
	assert.Contains(t, got, "- "+defaultURL+"@bundles/default")
	assert.Contains(t, got, "- "+personalURL+"@bundles/just")
	assert.NotContains(t, got, "@bundles/https:", "canonical URL must never be re-wrapped")
	assert.NotContains(t, got, "/https://", "canonical URL must never be prefixed")
}

// TestBundleRefCanonicalize_Idempotent verifies running the upgrade twice equals
// running it once — a second pass over already-canonical refs is a no-op.
func TestBundleRefCanonicalize_Idempotent(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - ctxloom-default/git\n")

	once, applied1 := runProfileUpgrade(personalURL, in)
	require.NotEmpty(t, applied1)

	twice, applied2 := runProfileUpgrade(personalURL, once)
	assert.Empty(t, applied2, "second pass over canonical refs must not fire")
	assert.Equal(t, string(once), string(twice))
}

// TestBundleRefCanonicalize_NoContextNoOp verifies that a profile with no
// resolution context (a local project profile: empty own URL, and refs whose
// alias the resolver doesn't know) keeps its bundle refs untouched.
func TestBundleRefCanonicalize_NoContextNoOp(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - unknown-alias/thing\n")

	out, applied := profileUpgrades("", testAliasToURL).Run(in)

	assert.Empty(t, applied, "no own URL + unknown alias => no canonicalization")
	assert.Equal(t, string(in), string(out))
}

// TestLoad_CanonicalizesShortBundlesViaResolver verifies the loader seam: a
// profile loaded under a name whose resolvers yield a remote comes back with its
// short bundle refs canonicalized, and records a pending upgrade for the rewrite
// prompt.
func TestLoad_CanonicalizesShortBundlesViaResolver(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/personal/go-developer.yaml",
		[]byte("description: test\nbundles:\n  - core-practices\n  - ctxloom-default/git\n"),
		0o644))

	resolver := func(name string) string {
		if name == "personal/go-developer" {
			return "personal"
		}
		return ""
	}
	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
		WithRemoteResolver(resolver), WithRemoteURLResolver(testAliasToURL))

	p, err := loader.Load("personal/go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{
		personalURL + "@bundles/core-practices",
		defaultURL + "@bundles/git",
	}, p.Bundles)

	pending := loader.PendingUpgrades()
	require.Len(t, pending, 1, "loader should record a pending rewrite")
	assert.Equal(t, "/profiles/personal/go-developer.yaml", pending[0].Path)
	assert.NotEmpty(t, pending[0].Applied)
}

// TestLoad_LocalProfileKeepsBareBundles verifies a profile whose resolver yields
// no remote (a local project profile) is loaded verbatim with no pending upgrade.
func TestLoad_LocalProfileKeepsBareBundles(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/go-developer.yaml",
		[]byte("bundles:\n  - local-bundle\n"),
		0o644))

	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
		WithRemoteResolver(func(string) string { return "" }),
		WithRemoteURLResolver(testAliasToURL))

	p, err := loader.Load("go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"local-bundle"}, p.Bundles)
	assert.Empty(t, loader.PendingUpgrades())
}

// TestCommitUpgrade_WritesCanonicalFileAndClearsPending verifies that persisting
// a pending upgrade rewrites the profile file to the canonical form and removes
// it from the loader's pending set.
func TestCommitUpgrade_WritesCanonicalFileAndClearsPending(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/profiles/personal/go-developer.yaml"
	require.NoError(t, afero.WriteFile(fs, path,
		[]byte("bundles:\n  - core-practices\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
		WithRemoteResolver(func(string) string { return "personal" }),
		WithRemoteURLResolver(testAliasToURL))

	_, err := loader.Load("personal/go-developer")
	require.NoError(t, err)
	pending := loader.PendingUpgrades()
	require.Len(t, pending, 1)

	require.NoError(t, loader.CommitUpgrade(pending[0]))

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(data), personalURL+"@bundles/core-practices")
	assert.Empty(t, loader.PendingUpgrades(), "committed pending should be cleared")
}

// TestCanonicalize_StripsLegacyV1FromBundlesAndParents verifies the directory
// normalization pass: a canonical ref carrying the dead "v1/" schema segment is
// collapsed to the new layout in BOTH `bundles:` and `parents:`, so the stored
// ref equals its CanonicalString. A version pin survives the rewrite.
func TestCanonicalize_StripsLegacyV1FromBundlesAndParents(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - " + defaultURL + "@v1/bundles/git\n" +
		"  - " + personalURL + "@v1/bundles/just@v1.2.3\n")

	out, applied := runProfileUpgrade(personalURL, in)

	require.NotEmpty(t, applied, "legacy v1 refs should be normalized")
	got := string(out)
	assert.Contains(t, got, "- "+defaultURL+"@bundles/git")
	// The content-version pin is preserved across the layout normalization.
	assert.Contains(t, got, "- "+personalURL+"@bundles/just@v1.2.3")
	// No "v1/" schema segment may survive.
	assert.NotContains(t, got, "@v1/")
}

// TestParentCanonicalize_LocalSiblingsUntouched verifies parents that are NOT
// canonical URLs — a bare local sibling name and an alias-prefixed local profile
// path — pass through verbatim. Resolving them against a remote alias would
// wrongly promote a local parent into a remote ref.
func TestParentCanonicalize_LocalSiblingsUntouched(t *testing.T) {
	in := []byte("parents:\n" +
		"  - base-profile\n" +
		"  - personal/prototype\n")

	out, applied := runProfileUpgrade(personalURL, in)

	assert.Empty(t, applied, "local parent refs must not be canonicalized")
	assert.Equal(t, string(in), string(out))
}

// TestParentCanonicalize_AlreadyCanonicalUntouched is an idempotency guard: a
// parent that is already a normalized canonical URL passes through unchanged.
func TestParentCanonicalize_AlreadyCanonicalUntouched(t *testing.T) {
	in := []byte("parents:\n  - " + defaultURL + "@profiles/rust-developer\n")

	out, applied := runProfileUpgrade(personalURL, in)

	assert.Empty(t, applied, "already-canonical parent must not fire the upgrade")
	assert.Equal(t, string(in), string(out))
}

// TestLoad_NoResolverIsNoOp verifies a loader constructed without remote
// resolvers behaves exactly as before — bare refs untouched, no panics.
func TestLoad_NoResolverIsNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/personal/go-developer.yaml",
		[]byte("bundles:\n  - core-practices\n"),
		0o644))

	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	p, err := loader.Load("personal/go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"core-practices"}, p.Bundles)
	assert.Empty(t, loader.PendingUpgrades())
}
