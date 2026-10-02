package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// Decision E (local-file-wins) on the canonicalize-on-STORE sites: a LOCAL
// bundle spelled "<alias>/<bundle>" — here <bundles>/personal/reviews while a
// "personal" remote is registered — is stored as the local name, not rewritten
// to the remote. The remote-only "personal/agent-ensemble" beside it is the
// control proving the alias is otherwise applied.
func TestStoreProfile_LocalBundleWinsOverSameSpelledAlias(t *testing.T) {
	root := t.TempDir()
	cfg := agentTestConfig(root, nil)
	appDir := cfg.GetAppPaths()[0]
	registerPersonalRemote(t, appDir)
	bundletree.WriteOS(t, paths.BundlesLayoutRoot(paths.LocalBundlesPath(appDir), paths.LayoutV2),
		"personal/reviews", "version: \"1.0\"\ndescription: local reviews\n")

	dir := filepath.Join(root, ".ctxloom", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0755))
	loader := profiles.NewLoader([]string{dir})
	want := []string{"personal/reviews", shortNamePersonalURL + "@bundles/agent-ensemble"}

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:    "created",
		Bundles: []string{"personal/reviews", "personal/agent-ensemble"},
		Loader:  loader,
	})
	require.NoError(t, err)
	created, err := loader.Load("created")
	require.NoError(t, err)
	assert.Equal(t, want, created.Bundles, "create")

	_, err = CreateProfile(context.Background(), cfg, CreateProfileRequest{Name: "updated", Bundles: []string{"core-practices"}, Loader: loader})
	require.NoError(t, err)
	_, err = UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "updated",
		AddBundles: []string{"personal/reviews", "personal/agent-ensemble"},
		Loader:     loader,
	})
	require.NoError(t, err)
	updated, err := loader.Load("updated")
	require.NoError(t, err)
	assert.Equal(t, append([]string{"core-practices"}, want...), updated.Bundles, "update")
}
