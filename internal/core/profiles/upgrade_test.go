package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

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

// runCanonicalize runs the alias stage a LOCAL bundle's profiles get
// (Loader.canonicalizeLocalAliases) over data, and reports the upgraded bytes
// plus which upgrades fired.
func runCanonicalize(t *testing.T, data []byte) ([]byte, []string) {
	t.Helper()
	return mustRun(t, upgrade.Pipeline{bundleRefCanonicalizeUpgrade{aliasToURL: testAliasToURL}}, data)
}

// TestBundleRefCanonicalize_ShortRefsBecomeCanonical verifies that
// alias-prefixed bundle refs are rewritten to canonical URL form against the
// alias's own repo, that a cherry-picked ":fragments/…" selector on one becomes
// the canonical "#fragments/…", and that a bare ref — a LOCAL bundle — is left
// as written.
func TestBundleRefCanonicalize_ShortRefsBecomeCanonical(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - core-practices\n" +
		"  - personal/developer-mindset\n" +
		"  - ctxloom-default/git\n" +
		"  - personal/go-development:fragments/testing\n")

	out, applied := runCanonicalize(t, in)

	require.NotEmpty(t, applied, "upgrade should fire when short refs are present")
	got := string(out)
	// Bare → a local bundle, as written.
	assert.Contains(t, got, "- core-practices\n")
	// Alias → that alias's repo.
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(personalURL+"@bundles/developer-mindset"))
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(defaultURL+"@bundles/git"))
	// Cherry-pick: bundle canonicalized, ':' selector normalized to '#'.
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(personalURL+"@bundles/go-development#fragments/testing"))
	// No short/alias form should survive.
	assert.NotContains(t, got, "- personal/")
	assert.NotContains(t, got, "- ctxloom-default/")
}

// TestBundleRefCanonicalize_CanonicalURLsUntouched is a regression guard: a
// URL ref is already fully qualified, so it is only re-spelled as its
// canonical URI, never re-resolved. The scheme colon in "https://" must NOT be
// mistaken for the cherry-pick ':' separator — doing so split the bundle name
// down to "https" and produced a nonsense "<remote>/https://…" ref that no
// longer resolved.
func TestBundleRefCanonicalize_CanonicalURLsUntouched(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - " + defaultURL + "@bundles/default\n" +
		"  - " + personalURL + "@bundles/just\n" +
		"  - core-practices\n")

	out, applied := runCanonicalize(t, in)

	got := string(out)
	assert.NotEmpty(t, applied, "URL refs are re-spelled canonically")
	assert.Contains(t, got, "- core-practices\n")
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(defaultURL+"@bundles/default"))
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(personalURL+"@bundles/just"))
	assert.NotContains(t, got, "@bundles/https:", "canonical URL must never be re-wrapped")
	assert.NotContains(t, got, "/https://", "canonical URL must never be prefixed")
}

// TestBundleRefCanonicalize_Idempotent verifies running the upgrade twice equals
// running it once — a second pass over already-canonical refs is a no-op.
func TestBundleRefCanonicalize_Idempotent(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - ctxloom-default/git\n")

	once, applied1 := runCanonicalize(t, in)
	require.NotEmpty(t, applied1)

	twice, applied2 := runCanonicalize(t, once)
	assert.Empty(t, applied2, "second pass over canonical refs must not fire")
	assert.Equal(t, string(once), string(twice))
}

// TestBundleRefCanonicalize_NoContextNoOp verifies that refs the alias
// resolver cannot place — a bare local bundle, an unknown alias — stay
// untouched.
func TestBundleRefCanonicalize_NoContextNoOp(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - unknown-alias/thing\n")

	out, applied := runCanonicalize(t, in)

	assert.Empty(t, applied, "bare ref + unknown alias => no canonicalization")
	assert.Equal(t, string(in), string(out))
}

// TestLoader_CanonicalizesLocalBundleProfileAliases verifies the loader seam:
// a LOCAL bundle's profile comes back with its "<alias>/<bundle>" refs
// canonicalized against the alias's repo, its bare refs (local bundles) as
// written.
func TestLoader_CanonicalizesLocalBundleProfileAliases(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProjectProfile(t, fs, "go-developer", "description: test\nbundles:\n  - core-practices\n  - ctxloom-default/git\n")

	loader := bundleLoader(t, fs, WithRemoteURLResolver(testAliasToURL))

	p, err := loader.Load("go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"core-practices", remote.CanonicalSpelling(defaultURL + "@bundles/git")}, p.Bundles)
}

// TestLoader_RemoteBundleProfileAliasesUntouched verifies the alias stage is
// LOCAL-only: a remote bundle's profile was not written against this
// machine's alias table, so its refs are left as they arrived.
func TestLoader_RemoteBundleProfileAliasesUntouched(t *testing.T) {
	key := seedKey(defaultURL, "kit", "dev")
	seed := map[string]*Profile{key: {Name: key, Path: SeededProfilePathPrefix + key, Bundles: []string{"ctxloom-default/git"}}}

	loader := NewLoader(nil, WithSeededProfiles(seed), WithRemoteURLResolver(testAliasToURL))

	p, err := loader.Load(key)
	require.NoError(t, err)
	assert.Equal(t, []string{"ctxloom-default/git"}, p.Bundles)
}

// TestLoad_LocalProfileKeepsBareBundles verifies a local profile's bare
// bundle refs — local bundles — load verbatim.
func TestLoad_LocalProfileKeepsBareBundles(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProjectProfile(t, fs, "go-developer", "bundles:\n  - local-bundle\n")

	loader := bundleLoader(t, fs, WithRemoteURLResolver(testAliasToURL))

	p, err := loader.Load("go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"local-bundle"}, p.Bundles)
}

// TestCanonicalize_StripsLegacyV1FromBundlesAndParents verifies the directory
// normalization pass: a canonical ref carrying the dead "v1/" schema segment is
// collapsed to the new layout in BOTH `bundles:` and `parents:`, so the stored
// ref equals its CanonicalString. A version pin survives the rewrite.
func TestCanonicalize_StripsLegacyV1FromBundlesAndParents(t *testing.T) {
	in := []byte("bundles:\n" +
		"  - " + defaultURL + "@v1/bundles/git\n" +
		"  - " + personalURL + "@v1/bundles/just@v1.2.3\n")

	out, applied := runCanonicalize(t, in)

	require.NotEmpty(t, applied, "legacy v1 refs should be normalized")
	got := string(out)
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(defaultURL+"@bundles/git"))
	// The content-version pin is preserved across the layout normalization.
	assert.Contains(t, got, "- "+remote.CanonicalSpelling(personalURL+"@bundles/just@v1.2.3"))
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

	out, applied := runCanonicalize(t, in)

	assert.Empty(t, applied, "local parent refs must not be canonicalized")
	assert.Equal(t, string(in), string(out))
}

// TestParentCanonicalize_AlreadyCanonicalUntouched is an idempotency guard: a
// parent that is already a normalized canonical URL passes through unchanged.
func TestParentCanonicalize_AlreadyCanonicalUntouched(t *testing.T) {
	in := []byte("parents:\n  - " + defaultURL + "@profiles/rust-developer\n")

	out, applied := runCanonicalize(t, in)

	assert.Empty(t, applied, "already-canonical parent must not fire the upgrade")
	assert.Equal(t, string(in), string(out))
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

// seedKey is the key the config seed files a bundle profile under: the
// bundle's canonical identity plus "#profiles/<name>".
func seedKey(repoURL, bundle, profile string) string {
	key, err := remote.BundleProfileRef(repoURL+"@bundles/"+bundle, profile)
	if err != nil {
		panic(err)
	}
	return key
}

// TestBundleRefCanonicalize_LocalBundleWinsOverSameSpelledAlias pins decision E
// (local-file-wins) on the profile upgrade: a LOCAL bundle whose directory name
// begins with a configured remote alias ("personal/reviews") is not rewritten to
// that remote, and the upgrade does not fire for it — otherwise the user's
// profile is migrated on disk to name a different bundle, or none.
func TestBundleRefCanonicalize_LocalBundleWinsOverSameSpelledAlias(t *testing.T) {
	local := func(base string) bool { return base == "personal/reviews" }

	t.Run("only a local ref: nothing fires", func(t *testing.T) {
		in := []byte("bundles:\n  - personal/reviews\n  - personal/reviews#fragments/x\n")
		out, applied := mustRun(t, upgrade.Pipeline{bundleRefCanonicalizeUpgrade{aliasToURL: testAliasToURL, localBundleExists: local}}, in)
		assert.Empty(t, applied, "a local bundle must not stage an on-disk migration")
		assert.Equal(t, string(in), string(out))
	})

	t.Run("a non-local alias ref beside it still canonicalizes", func(t *testing.T) {
		in := []byte("bundles:\n  - personal/reviews\n  - personal/developer-mindset\n")
		out, applied := mustRun(t, upgrade.Pipeline{bundleRefCanonicalizeUpgrade{aliasToURL: testAliasToURL, localBundleExists: local}}, in)
		require.NotEmpty(t, applied)
		got := string(out)
		assert.Contains(t, got, "- personal/reviews\n")
		assert.Contains(t, got, "- "+remote.CanonicalSpelling(personalURL+"@bundles/developer-mindset"))
	})
}

// mustRun runs p over data and fails the test on an encode error, which none
// of these fixtures can produce.
func mustRun(t *testing.T, p upgrade.Pipeline, data []byte) ([]byte, []string) {
	t.Helper()
	out, applied, err := p.Run(data)
	require.NoError(t, err)
	return out, applied
}
