package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

const shortNamePersonalURL = "https://github.com/ben/ctxloom-personal"

// registerPersonalRemote writes a remotes.yaml under the config's .ctxloom that
// maps the "personal" alias to a repo URL, so the canonicalize-on-store choke can
// resolve it.
func registerPersonalRemote(t *testing.T, appPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appPath, 0755))
	reg, err := remote.NewRegistry(paths.RemotesPath(appPath))
	require.NoError(t, err)
	require.NoError(t, reg.Add("personal", shortNamePersonalURL))
}

// TestSetAgent_CanonicalizesShortProfiles pins decision B for agent bindings: a
// short "<remote>/<bundle>#profiles/<name>" profile is stored as its canonical
// URL, while a bare local name is stored verbatim.
func TestSetAgent_CanonicalizesShortProfiles(t *testing.T) {
	root := t.TempDir()
	cfg := agentTestConfig(root, nil)
	registerPersonalRemote(t, cfg.GetAppPaths()[0])

	res, err := SetAgent(cfg, SetAgentRequest{
		Name:     "dev",
		Profiles: []string{"personal/agent-ensemble#profiles/finder", "developer"},
	})
	require.NoError(t, err)

	want := []string{shortNamePersonalURL + "@bundles/agent-ensemble#profiles/finder", "developer"}
	assert.Equal(t, want, res.Profiles, "result reflects the canonical stored form")
	assert.Equal(t, want, cfg.GetConfiguredAgents()["dev"].Profiles, "binding persists canonical, not the alias")
}

// TestCreateProfile_CanonicalizesShortRefs pins decision B for profiles: a short
// "<remote>/<bundle>" bundle and a short "<remote>/<bundle>#profiles/<name>"
// parent are stored canonical; a bare bundle name stays local (decision A).
func TestCreateProfile_CanonicalizesShortRefs(t *testing.T) {
	root := t.TempDir()
	cfg := agentTestConfig(root, nil)
	registerPersonalRemote(t, cfg.GetAppPaths()[0])

	dir := filepath.Join(root, ".ctxloom", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0755))
	loader := profiles.NewLoader([]string{dir})

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:    "dev",
		Bundles: []string{"personal/agent-ensemble", "core-practices"},
		Parents: []string{"personal/agent-ensemble#profiles/finder"},
		Loader:  loader,
	})
	require.NoError(t, err)

	saved, err := loader.Load("dev")
	require.NoError(t, err)
	assert.Equal(t, []string{
		shortNamePersonalURL + "@bundles/agent-ensemble", // alias bundle → canonical
		"core-practices", // bare name → local, verbatim
	}, saved.Bundles)
	assert.Equal(t, []string{
		shortNamePersonalURL + "@bundles/agent-ensemble#profiles/finder", // alias parent → canonical
	}, saved.Parents)
}
