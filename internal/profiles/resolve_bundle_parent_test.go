package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seededParentLoader returns a loader over memfs with one fs profile ("dev")
// whose parent is the given ref, seeded with a bundle profile under its
// canonical "<bundle>#profiles/<name>" key.
func seededParentLoader(t *testing.T, parentRef string) *Loader {
	t.Helper()
	key := defaultURI + "//bundles/ai-developer#profiles/developer"
	seed := map[string]*Profile{
		key: {
			Name:    key,
			Path:    SeededProfilePathPrefix + key,
			Bundles: []string{defaultURL + "@bundles/testing"},
		},
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/dev.yaml",
		[]byte("parents:\n  - "+parentRef+"\nbundles:\n  - own-bundle\n"), 0644))
	return NewLoader([]string{"/profiles"}, WithFS(fs), WithSeededProfiles(seed))
}

// TestResolveProfile_BundleProfileParent is the regression pin for resolving a
// directory profile whose parent is a bundle-shipped profile: the parent name
// was canonicalized through CanonicalKey, which drops the "#profiles/<name>"
// selector and collapses the parent to its bundle — so every bundle-profile
// parent resolved as not installed and its content was silently skipped.
func TestResolveProfile_BundleProfileParent(t *testing.T) {
	loader := seededParentLoader(t, defaultURL+"@bundles/ai-developer#profiles/developer")

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.Contains(t, resolved.Bundles, defaultURL+"@bundles/testing",
		"parent bundle-profile content must be merged")
	assert.Contains(t, resolved.Bundles, "own-bundle")
}

// TestResolveProfile_BundleProfileParentPinned confirms a "@<sha>"-pinned
// bundle-profile parent resolves to the same version-agnostic seed entry.
func TestResolveProfile_BundleProfileParentPinned(t *testing.T) {
	loader := seededParentLoader(t, defaultURL+"@bundles/ai-developer@abc1234#profiles/developer")

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.Contains(t, resolved.Bundles, defaultURL+"@bundles/testing",
		"pinned parent must resolve via its version-agnostic canonical key")
}

// TestLoad_PinnedBundleProfileRefHitsSeed pins lookupSeeded's selector-
// preserving fallback: a pinned bundle-profile ref finds the version-agnostic
// seed entry directly.
func TestLoad_PinnedBundleProfileRefHitsSeed(t *testing.T) {
	loader := seededParentLoader(t, defaultURL+"@bundles/ai-developer#profiles/developer")

	p, err := loader.Load(defaultURL + "@bundles/ai-developer@abc1234#profiles/developer")
	require.NoError(t, err)
	assert.Equal(t, defaultURI+"//bundles/ai-developer#profiles/developer", p.Name)
}
