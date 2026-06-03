package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runProfileUpgrade is a helper: run the profile upgrade pipeline for remote
// over data and report the upgraded bytes plus which upgrades fired.
func runProfileUpgrade(remote string, data []byte) ([]byte, []string) {
	return profileUpgrades(remote).Run(data)
}

// TestBundleRefQualify_BareRefsGainRemote verifies that whole-bundle refs with
// no remote are qualified with the profile's remote, while already-qualified
// refs and cherry-picked items keep their bundle's existing remote.
func TestBundleRefQualify_BareRefsGainRemote(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - personal/already-qualified\n  - go-development:fragments/testing\n")

	out, applied := runProfileUpgrade("personal", in)

	require.NotEmpty(t, applied, "upgrade should fire when bare refs are present")
	got := string(out)
	assert.Contains(t, got, "personal/core-practices")
	assert.Contains(t, got, "personal/already-qualified")
	assert.NotContains(t, got, "personal/personal/already-qualified", "must not double-qualify")
	// Cherry-picked item: only the bundle portion (before ':') is qualified.
	assert.Contains(t, got, "personal/go-development:fragments/testing")
}

// TestBundleRefQualify_Idempotent verifies running the upgrade twice equals
// running it once — a second pass over already-qualified refs is a no-op.
func TestBundleRefQualify_Idempotent(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n  - containers\n")

	once, applied1 := runProfileUpgrade("personal", in)
	require.NotEmpty(t, applied1)

	twice, applied2 := runProfileUpgrade("personal", once)
	assert.Empty(t, applied2, "second pass over qualified refs must not fire")
	assert.Equal(t, string(once), string(twice))
}

// TestBundleRefQualify_EmptyRemoteNoOp verifies that a profile with no resolvable
// remote (local project profile) keeps its bare bundle refs untouched.
func TestBundleRefQualify_EmptyRemoteNoOp(t *testing.T) {
	in := []byte("bundles:\n  - core-practices\n")

	out, applied := runProfileUpgrade("", in)

	assert.Empty(t, applied, "no remote => no qualification")
	assert.Equal(t, string(in), string(out))
}

// TestLoad_QualifiesBareBundlesViaResolver verifies the loader seam: a profile
// loaded under a name whose resolver yields a remote comes back with its bare
// bundle refs qualified, and records a pending upgrade for the rewrite prompt.
func TestLoad_QualifiesBareBundlesViaResolver(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/personal/go-developer.yaml",
		[]byte("description: test\nbundles:\n  - core-practices\n  - go-development\n"),
		0o644))

	resolver := func(name string) string {
		if name == "personal/go-developer" {
			return "personal"
		}
		return ""
	}
	loader := NewLoader([]string{"/profiles"}, WithFS(fs), WithRemoteResolver(resolver))

	p, err := loader.Load("personal/go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"personal/core-practices", "personal/go-development"}, p.Bundles)

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
		WithRemoteResolver(func(string) string { return "" }))

	p, err := loader.Load("go-developer")
	require.NoError(t, err)
	assert.Equal(t, []string{"local-bundle"}, p.Bundles)
	assert.Empty(t, loader.PendingUpgrades())
}

// TestCommitUpgrade_WritesQualifiedFileAndClearsPending verifies that persisting
// a pending upgrade rewrites the profile file to the qualified form and removes
// it from the loader's pending set.
func TestCommitUpgrade_WritesQualifiedFileAndClearsPending(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/profiles/personal/go-developer.yaml"
	require.NoError(t, afero.WriteFile(fs, path,
		[]byte("bundles:\n  - core-practices\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
		WithRemoteResolver(func(string) string { return "personal" }))

	_, err := loader.Load("personal/go-developer")
	require.NoError(t, err)
	pending := loader.PendingUpgrades()
	require.Len(t, pending, 1)

	require.NoError(t, loader.CommitUpgrade(pending[0]))

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "personal/core-practices")
	assert.Empty(t, loader.PendingUpgrades(), "committed pending should be cleared")
}

// TestLoad_NoResolverIsNoOp verifies a loader constructed without a remote
// resolver behaves exactly as before — bare refs untouched, no panics.
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
