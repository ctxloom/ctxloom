package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// TestPromptSelectorUpgrade_MigratesSelectors pins the prompt→command selector
// migration: legacy "#prompts/" and ":prompts/" item selectors in a profile's
// bundles/bundle_items are rewritten to the commands section on load.
func TestPromptSelectorUpgrade_MigratesSelectors(t *testing.T) {
	in := []byte("name: p\nbundles:\n  - core#prompts/review\n  - alias/other:prompts/lint\nbundle_items:\n  - b#prompts/x\n")
	out, applied := upgrade.Pipeline{promptSelectorUpgrade{}}.Run(in)
	require.NotEmpty(t, applied, "migration should fire on legacy prompt selectors")
	s := string(out)
	assert.Contains(t, s, "core#commands/review")
	assert.Contains(t, s, "other:commands/lint")
	assert.Contains(t, s, "b#commands/x")
	assert.NotContains(t, s, "prompts/", "no prompt selector should remain")
}

// TestPromptSelectorUpgrade_Idempotent confirms a profile already on the
// commands vocabulary is left untouched.
func TestPromptSelectorUpgrade_Idempotent(t *testing.T) {
	in := []byte("name: p\nbundles:\n  - core#commands/review\n")
	_, applied := upgrade.Pipeline{promptSelectorUpgrade{}}.Run(in)
	assert.Empty(t, applied, "commands-vocabulary profile must not change")
}

// personalURL and defaultURL are the canonical repo URLs the test alias resolver
// maps the two stock remotes to.
const (
	personalURL = "https://github.com/benjaminabbitt/ctxloom-personal"
	defaultURL  = "https://github.com/ctxloom/ctxloom-default"
)

// defaultURI and personalURI are the same two remotes spelled as the canonical
// URI scheme — the identity a bundle ref from either one resolves to, and
// therefore what a seed key and a resolved SourceRef are.
const (
	personalURI = "ctxloom+git://github.com/benjaminabbitt/ctxloom-personal"
	defaultURI  = "ctxloom+git://github.com/ctxloom/ctxloom-default"
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
// whose own remote URL is ownURL over data (no bundle-profile seed), and report
// the upgraded bytes plus which upgrades fired.
func runProfileUpgrade(ownURL string, data []byte) ([]byte, []string) {
	return profileUpgrades(ownURL, testAliasToURL, nil).Run(data)
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

	out, applied := profileUpgrades("", testAliasToURL, nil).Run(in)

	assert.Empty(t, applied, "no own URL + unknown alias => no canonicalization")
	assert.Equal(t, string(in), string(out))
}

// testBundleProfileSeed is the seeded bundle-profile map the retired-parent
// upgrade discovers successors in: keys are the canonical
// "<bundle>#profiles/<name>" refs the config bundle-profile seed produces.
func testBundleProfileSeed() map[string]*Profile {
	return map[string]*Profile{
		defaultURL + "@bundles/ai-developer#profiles/developer": {},
		defaultURL + "@bundles/default#profiles/default":        {},
	}
}

// TestRetiredParentUpgrade_RewritesToBundleProfile verifies a parent in the
// retired top-level "@profiles/" grammar is rewritten to the one seeded bundle
// profile its repo ships under that name.
func TestRetiredParentUpgrade_RewritesToBundleProfile(t *testing.T) {
	in := []byte("parents:\n  - " + defaultURL + "@profiles/developer\n")

	out, applied := profileUpgrades(personalURL, testAliasToURL, testBundleProfileSeed()).Run(in)

	require.NotEmpty(t, applied, "retired parent should fire the upgrade")
	got := string(out)
	assert.Contains(t, got, "- "+defaultURL+"@bundles/ai-developer#profiles/developer")
	assert.NotContains(t, got, "@profiles/", "no retired-grammar ref should remain")
}

// TestRetiredParentUpgrade_DropsVersionPin verifies a "@<version>"-pinned
// retired parent still discovers its successor — the pin is dropped because the
// successor pins via the bundle's lockfile entry.
func TestRetiredParentUpgrade_DropsVersionPin(t *testing.T) {
	in := []byte("parents:\n  - " + defaultURL + "@profiles/developer@abc1234\n")

	out, applied := profileUpgrades(personalURL, testAliasToURL, testBundleProfileSeed()).Run(in)

	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "- "+defaultURL+"@bundles/ai-developer#profiles/developer")
}

// TestRetiredParentUpgrade_UnmatchedLeftVerbatim verifies fault tolerance: a
// retired parent whose profile no installed bundle ships (dropped upstream, or
// the successor bundle not yet pulled) is persisted as authored so the resolver
// can warn, rather than guessed at.
func TestRetiredParentUpgrade_UnmatchedLeftVerbatim(t *testing.T) {
	in := []byte("parents:\n  - " + defaultURL + "@profiles/go-developer\n")

	out, applied := profileUpgrades(personalURL, testAliasToURL, testBundleProfileSeed()).Run(in)

	assert.Empty(t, applied, "unmatched retired parent must not fire any upgrade")
	assert.Equal(t, string(in), string(out))
}

// TestRetiredParentUpgrade_AmbiguousLeftVerbatim verifies that two bundles from
// the same repo shipping the same profile name block the rewrite — a migration
// must not guess between them.
func TestRetiredParentUpgrade_AmbiguousLeftVerbatim(t *testing.T) {
	seed := testBundleProfileSeed()
	seed[defaultURL+"@bundles/other-kit#profiles/developer"] = &Profile{}
	in := []byte("parents:\n  - " + defaultURL + "@profiles/developer\n")

	out, applied := profileUpgrades(personalURL, testAliasToURL, seed).Run(in)

	assert.Empty(t, applied, "ambiguous successor must not fire any upgrade")
	assert.Equal(t, string(in), string(out))
}

// TestRetiredParentUpgrade_Idempotent verifies successor-form and local parents
// never re-fire the upgrade.
func TestRetiredParentUpgrade_Idempotent(t *testing.T) {
	in := []byte("parents:\n" +
		"  - " + defaultURL + "@bundles/ai-developer#profiles/developer\n" +
		"  - base\n")

	out, applied := profileUpgrades(personalURL, testAliasToURL, testBundleProfileSeed()).Run(in)

	assert.Empty(t, applied, "successor-form and local parents must not change")
	assert.Equal(t, string(in), string(out))
}

// TestFindBundleProfileKey pins the discovery rule the retired-parent rewrite
// (and the config seed post-pass) share: exactly one bundle from the ref's repo
// shipping the profile name — none and ambiguity both yield false.
func TestFindBundleProfileKey(t *testing.T) {
	seed := testBundleProfileSeed()

	key, ok := FindBundleProfileKey(seed, defaultURL, "developer")
	assert.True(t, ok)
	assert.Equal(t, defaultURL+"@bundles/ai-developer#profiles/developer", key)

	_, ok = FindBundleProfileKey(seed, defaultURL, "missing")
	assert.False(t, ok, "unknown profile name must not match")

	_, ok = FindBundleProfileKey(seed, personalURL, "developer")
	assert.False(t, ok, "a different repo's profile must not match")

	seed[defaultURL+"@bundles/other-kit#profiles/developer"] = &Profile{}
	_, ok = FindBundleProfileKey(seed, defaultURL, "developer")
	assert.False(t, ok, "ambiguity must not match")
}

// TestLoad_RewritesRetiredParentViaSeed verifies the loader seam: a directory
// profile whose parent uses the retired "@profiles/" grammar comes back with
// the parent rewritten to the seeded bundle profile, and the rewrite is
// recorded as pending for the consented on-disk migration.
func TestLoad_RewritesRetiredParentViaSeed(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/dev.yaml",
		[]byte("parents:\n  - "+defaultURL+"@profiles/developer\n"), 0644))

	loader := NewLoader([]string{"/profiles"},
		WithFS(fs),
		WithSeededProfiles(testBundleProfileSeed()))

	p, err := loader.Load("dev")
	require.NoError(t, err)
	require.Len(t, p.Parents, 1)
	assert.Equal(t, defaultURL+"@bundles/ai-developer#profiles/developer", p.Parents[0])
	assert.NotEmpty(t, loader.PendingUpgrades(), "rewrite should be staged for consent")
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

// TestUpgradeLedger_IsWiredToStorageAndToTheSeed pins the couplings that make
// the loader's schema-upgrade ledger part of the loader rather than a passenger
// on it: the ledger is PRODUCED by the storage read path (loadFile), it is
// COMMITTED through the loader's own filesystem, and its rewrites are DISCOVERED
// from the loader's seed registry. Extracting the ledger to its own type would
// have to carry all three, so the test is what makes that cost visible.
func TestUpgradeLedger_IsWiredToStorageAndToTheSeed(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/profiles/dev.yaml"
	require.NoError(t, afero.WriteFile(fs, path,
		[]byte("parents:\n  - "+defaultURL+"@profiles/developer\n"), 0o644))

	loader := NewLoader([]string{"/profiles"},
		WithFS(fs),
		WithSeededProfiles(testBundleProfileSeed()))

	// Produced by the storage read path, and only by it.
	require.Empty(t, loader.PendingUpgrades(), "no ledger entry before anything is read")
	_, err := loader.Load("dev")
	require.NoError(t, err)
	pending := loader.PendingUpgrades()
	require.Len(t, pending, 1)

	// Discovered through the SEED registry: the retired @profiles/ parent is
	// rewritten to the bundle-shipped successor the seed map ships, which a
	// ledger with no view of the seed could not have found.
	assert.Contains(t, string(pending[0].Data), defaultURL+"@bundles/ai-developer#profiles/developer")

	// Committed through the loader's OWN filesystem, not the OS one.
	require.NoError(t, loader.CommitUpgrade(pending[0]))
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(data), defaultURL+"@bundles/ai-developer#profiles/developer")
}

// TestSplitBundleSelector_LegacyMarkersAreTheColonEraSections pins which
// selectors the legacy ':' grammar had. The list is exactly the sections that
// existed while ':' was the separator -- fragments, commands, mcp -- and it is
// deliberately identical to bundles.expandBundleRef's, which this splitter
// mirrors. Skills are NOT among them and must not be added: the skills item kind
// postdates the ':' grammar entirely, so no profile on disk can carry
// "<bundle>:skills/<name>", and adding the marker here alone would split a
// selector the bundle expander still cannot.
func TestSplitBundleSelector_LegacyMarkersAreTheColonEraSections(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantBase string
		wantItem string
	}{
		{"legacy fragments", "personal/git:fragments/x", "personal/git", "#fragments/x"},
		{"legacy commands", "personal/git:commands/x", "personal/git", "#commands/x"},
		{"legacy mcp", "personal/git:mcp", "personal/git", "#mcp"},
		{"canonical skills selector", "personal/git#skills/x", "personal/git", "#skills/x"},
		{"colon-spelled skills is not a legacy selector", "personal/git:skills/x", "personal/git:skills/x", ""},
		{"no selector", "personal/git", "personal/git", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, item := splitBundleSelector(tt.ref)
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.wantItem, item)
		})
	}
}
