package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// TestProfileOperations_ListCreateDelete_RoundTrip drives the authoring
// operations end to end against an injected loader, touching no real disk.
//
// This used to be the ADR-0026 port test, proving the operations depended on
// profiles.Store rather than a concrete loader, with an in-memory MemStore as
// the second adapter. The port was retired by owner ruling: there was one real
// adapter and one test double, and the double's only consumer outside the
// package was the port test itself — an abstraction whose sole client was the
// test proving the abstraction.
//
// What that test also covered, and what is kept here, is the ROUND TRIP: a
// create is visible to a subsequent read, and a delete removes it. That is a
// claim about the operations, not about the port, and it survives the port.
// The filesystem-free property survives too, through profiles.WithFS.
func TestProfileOperations_ListCreateDelete_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/app/" + paths.AppDirName
	dir := paths.ProfilesPath(appDir)
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	for _, name := range []string{"alpha", "beta"} {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, name+".yaml"), []byte("description: seeded\n"), 0o644))
	}
	loader := profiles.NewLoader([]string{dir}, profiles.WithFS(fs))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	cfg.SetFS(fs)

	list, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, 2, list.Count, "both seeded profiles are listed")

	// A profile needs content: the loader refuses to write an empty one. The
	// retired MemStore double did not enforce that, so the port test this
	// replaces was passing against a store more permissive than production.
	_, err = CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name: "gamma", Loader: loader, Bundles: []string{"some-bundle"},
	})
	require.NoError(t, err)
	assert.True(t, loader.Exists("gamma"), "a created profile must be visible to a subsequent read")

	_, err = DeleteProfile(context.Background(), cfg, DeleteProfileRequest{Name: "alpha", Loader: loader})
	require.NoError(t, err)
	assert.False(t, loader.Exists("alpha"), "a deleted profile must be gone from the same loader")

	// The control: the round trip is not passing because everything reads
	// empty. `beta` was neither created nor deleted and must be untouched.
	assert.True(t, loader.Exists("beta"), "an untouched profile must survive both operations")
}
