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
// internal/lm/backends/managed.go's gateProfileMCP/gateProfileHooks now key
// the executable trust gate by, instead of the display name.
func TestResolveProfile_SourceRef_BundleShippedRemote(t *testing.T) {
	key := defaultURL + "@bundles/kit#profiles/dev"
	seed := map[string]*Profile{
		key: {Name: key, Path: SeededProfilePathPrefix + key, Signer: "vendor@example.com"},
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile(key, nil)
	require.NoError(t, err)
	assert.Equal(t, defaultURL+"@bundles/kit", resolved.SourceRef,
		"SourceRef must be the origin bundle's canonical ref, with the #profiles/<name> selector stripped")
	assert.Equal(t, "vendor@example.com", resolved.Signer,
		"the seeded Profile.Signer (the origin bundle's verified publisher) must flow through to ResolvedProfile.Signer")
}

// TestResolveProfile_SourceRef_BundleShippedLocal proves a LOCAL bundle's
// shipped profile ("ctxloom:local@bundles/<name>#profiles/<p>", what
// config.loadBundleProfileSeed produces for an fs-installed bundle) resolves
// SourceRef to the ctxloom:local form — which trust.ParseItemRef still parses
// as IsLocal:true (parseLocalReference), so a local bundle's own
// directly-declared hook stays honestly local, exactly like before this fix.
func TestResolveProfile_SourceRef_BundleShippedLocal(t *testing.T) {
	key := "ctxloom:local@bundles/kit#profiles/dev"
	seed := map[string]*Profile{
		key: {Name: key, Path: SeededProfilePathPrefix + key}, // unsigned local bundle: no Signer
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile(key, nil)
	require.NoError(t, err)
	assert.Equal(t, "ctxloom:local@bundles/kit", resolved.SourceRef)
	assert.Empty(t, resolved.Signer, "an unsigned local bundle carries no verified signer")
}

// TestResolveProfile_SourceRef_GenuinelyLocalIsEmpty proves a bare-named,
// genuinely local/project-authored profile (.ctxloom/profiles/<name>.yaml, no
// "#profiles/" selector anywhere in its identity) gets an EMPTY SourceRef —
// the signal profileGateRefFor (managed.go) falls back to the bare
// profileName for, which trust.ParseItemRef resolves IsLocal:true. This must
// stay true: it is what keeps a genuinely local profile's inline hooks/mcp
// auto-allowed after the fix, exactly as before it.
func TestResolveProfile_SourceRef_GenuinelyLocalIsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/dev.yaml", []byte("description: local dev profile\n"), 0644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.Empty(t, resolved.SourceRef, "a genuinely local profile must carry no SourceRef")
	assert.Empty(t, resolved.Signer)
}

// TestResolveProfile_SourceRef_ChildNeverInheritsParentSource proves a
// profile's SourceRef/Signer are its OWN provenance, never a parent's: a
// genuinely local child profile that inherits from a bundle-shipped remote
// parent must still key its OWN directly-declared hooks/mcp as local — a
// parent's remote origin must never leak onto the child's gate identity (that
// would either wrongly deny the child's own local content, or — the more
// dangerous direction — wrongly key some FUTURE parent-sourcing bug as local).
func TestResolveProfile_SourceRef_ChildNeverInheritsParentSource(t *testing.T) {
	parentKey := defaultURL + "@bundles/kit#profiles/base"
	seed := map[string]*Profile{
		parentKey: {Name: parentKey, Path: SeededProfilePathPrefix + parentKey, Signer: "vendor@example.com"},
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/child.yaml",
		[]byte("parents:\n  - "+parentKey+"\ndescription: local child\n"), 0644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs), WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)
	assert.Empty(t, resolved.SourceRef, "the child's own SourceRef must stay empty (genuinely local) despite a remote-sourced parent")
	assert.Empty(t, resolved.Signer)
}
