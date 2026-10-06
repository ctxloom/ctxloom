package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveProfile_SourceRef_BundleShippedRemote proves ResolvedProfile.
// SourceRef (the ugly-sake/uncut-grub fix's provenance seam) is populated
// from the origin bundle's canonical ref, WITHOUT the "#profiles/<name>"
// selector, for a profile seeded under its "<bundle>#profiles/<name>" key —
// exactly what config.loadBundleProfileSeed produces for a bundle shipped by
// a remote (non-local) source. This is the ref
// managedhooks' gateProfileMCP/gateProfileHooks now key
// the executable trust gate by, instead of the display name.
func TestResolveProfile_SourceRef_BundleShippedRemote(t *testing.T) {
	key := defaultURI + "//bundles/kit#profiles/dev"
	seed := map[string]*Profile{
		key: {Name: key, Path: SeededProfilePathPrefix + key},
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile(key, nil)
	require.NoError(t, err)
	assert.Equal(t, defaultURI+"//bundles/kit", resolved.SourceRef,
		"SourceRef must be the origin bundle's canonical ref, with the #profiles/<name> selector stripped")
}

// TestResolveProfile_SourceRef_BundleShippedLocal proves a LOCAL bundle's
// shipped profile ("ctxloom:local@bundles/<name>#profiles/<p>", what
// config.loadBundleProfileSeed produces for an fs-installed bundle) resolves
// SourceRef to the ctxloom:local form — which remote.ParseReference still
// parses as IsLocal:true (parseLocalReference), so a local bundle's own
// directly-declared hook stays honestly local, exactly like before this fix.
func TestResolveProfile_SourceRef_BundleShippedLocal(t *testing.T) {
	key := "ctxloom+local:kit#profiles/dev"
	seed := map[string]*Profile{
		key: {Name: key, Path: SeededProfilePathPrefix + key},
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile(key, nil)
	require.NoError(t, err)
	assert.Equal(t, "ctxloom+local:kit", resolved.SourceRef)
}

// TestResolveProfile_SourceRef_ProjectProfileIsTheProjectBundle proves a
// project's own profile keys the exec gate by its OWN bundle — the project
// bundle — like every other bundle profile: there is no source-less profile
// left for the gate to treat specially.
func TestResolveProfile_SourceRef_ProjectProfileIsTheProjectBundle(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProjectProfile(t, fs, "dev", "description: local dev profile\n")
	loader := bundleLoader(t, fs)

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.Equal(t, "ctxloom+local:project", resolved.SourceRef)
}

// TestResolveProfile_SourceRef_ChildNeverInheritsParentSource proves a
// profile's SourceRef is its OWN provenance, never a parent's: a
// project profile that inherits from a bundle-shipped remote parent still keys
// its OWN directly-declared hooks/mcp by the project bundle — a parent's
// remote origin must never leak onto the child's gate identity.
func TestResolveProfile_SourceRef_ChildNeverInheritsParentSource(t *testing.T) {
	parentKey := defaultURL + "@bundles/kit#profiles/base"
	seed := map[string]*Profile{
		parentKey: {Name: parentKey, Path: SeededProfilePathPrefix + parentKey},
	}
	fs := afero.NewMemMapFs()
	writeProjectProfile(t, fs, "child", "parents:\n  - "+parentKey+"\ndescription: local child\n")

	loader := bundleLoader(t, fs, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)
	assert.Equal(t, "ctxloom+local:project", resolved.SourceRef, "the child keys by its own bundle, not its parent's")
}
