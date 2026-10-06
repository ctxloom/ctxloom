package config

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// remoteProfileConfig is a project whose only bundle is the pinned remote
// bundle kit of repoURL, shipping the profile dev.
func remoteProfileConfig(t *testing.T, repoURL string, dev profiles.Profile) (*Config, string) {
	t.Helper()
	const root = "/pinned"
	fsys := afero.NewMemMapFs()
	bundletree.WriteBundle(t, fsys, root, "kit", &bundles.Bundle{
		Version:  "1.0",
		Profiles: map[string]bundles.BundleProfile{"dev": dev},
	})
	tree, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	ref := repoURL + "@bundles/kit"
	reader := bundles.NewRepoFSReader(tree, ref, bundles.WithRepoURL(repoURL))

	cfg := NewBuilder(afero.NewMemMapFs(), true, "/proj/.ctxloom", SourceProject).Build()
	cfg.bindCatalog(func() bundles.Catalog { return bundles.Resolve(context.Background(), nil, reader) })

	seed := cfg.loadBundleProfileSeed()
	require.Len(t, seed, 1)
	for key := range seed {
		return cfg, key
	}
	return nil, ""
}

// The seed stamps a remote profile with the repository it was shipped in, so
// a remote profile reaching into another repository fails to load, by name.
func TestBundleProfileSeed_RemoteProfileReachingIntoAnotherRepoFails(t *testing.T) {
	const own = "https://example.test/acme/tools"
	const other = "https://example.test/evil/elsewhere@bundles/payload"
	cfg, key := remoteProfileConfig(t, own, profiles.Profile{Bundles: []string{"kit", other}})

	seeded := cfg.loadBundleProfileSeed()[key]
	assert.Equal(t, own, seeded.SourceURL, "a remote profile carries the repository it was shipped in")

	_, err := cfg.GetProfileLoader().Load(key)
	require.ErrorIs(t, err, profiles.ErrCrossRepoReference)
	assert.Contains(t, err.Error(), key, "the refusal names the profile")
	assert.Contains(t, err.Error(), other, "the refusal names the reference that broke the rule")
}

// A remote profile naming only its own repository's bundles — short or
// spelled in full — loads.
func TestBundleProfileSeed_RemoteProfileWithinItsRepoLoads(t *testing.T) {
	const own = "https://example.test/acme/tools"
	cfg, key := remoteProfileConfig(t, own, profiles.Profile{Bundles: []string{"kit", own + "@bundles/extra"}})

	p, err := cfg.GetProfileLoader().Load(key)
	require.NoError(t, err)
	assert.Len(t, p.Bundles, 2)
}

// A LOCAL bundle's profile is the project's own: it carries no source, and
// may draw on bundles of several registered repositories.
func TestBundleProfileSeed_LocalProfileCarriesNoSource(t *testing.T) {
	fs := afero.NewMemMapFs()
	bundletree.WriteDirProfiles(t, fs, "/proj/.ctxloom", map[string]any{
		"mix": Profile{Bundles: []string{"https://example.test/a/one@bundles/x", "https://example.test/b/two@bundles/y"}},
	})
	cfg := NewBuilder(fs, true, "/proj/.ctxloom", SourceProject).Build()

	p, err := cfg.GetProfileLoader().Load("mix")
	require.NoError(t, err)
	assert.Empty(t, p.SourceURL)
	assert.Len(t, p.Bundles, 2)
}
