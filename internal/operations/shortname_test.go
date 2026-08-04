package operations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	appDir := cfg.GetAppPaths()[0]
	registerPersonalRemote(t, appDir)
	mgr := config.NewManager(config.WithAppDir(appDir))

	res, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:     "dev",
		Profiles: ptr([]string{"personal/agent-ensemble#profiles/finder", "developer"}),
	})
	require.NoError(t, err)

	want := []string{shortNamePersonalURL + "@bundles/agent-ensemble#profiles/finder", "developer"}
	assert.Equal(t, want, res.Profiles, "result reflects the canonical stored form")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, want, reloaded.GetConfiguredAgents()["dev"].Profiles, "binding persists canonical, not the alias")
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
	loader := profiles.NewLoader(NewProjectReader(nil, []string{dir}))

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

// TestRegistryAliasToURL_UnknownAliasWarns: an alias-shaped ref
// ("<alias>/<path>") whose alias the registry does not know used to resolve to
// "" (ref kept as authored) with no signal anywhere. The fallback behavior is
// unchanged — resolution still fails safe and the on-disk format is untouched
// — but the failure must now be visible, since this is exactly the case a
// typo'd or unregistered alias silently persists a machine-local spelling.
func TestRegistryAliasToURL_UnknownAliasWarns(t *testing.T) {
	root := t.TempDir()
	reg, err := remote.NewRegistry(paths.RemotesPath(root))
	require.NoError(t, err)

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resolve := registryAliasToURL(reg)
	require.NotNil(t, resolve)
	got := resolve("nope")

	assert.Equal(t, "", got, "an unknown alias still resolves to \"\" — fault-tolerant, format unchanged")
	assert.Contains(t, buf.String(), "nope", "the unknown alias must be named in the warning")
}

// TestRegistryAliasToURL_KnownAliasNeverWarns is the healthy-path regression
// net for the fix above: resolving a REGISTERED alias must stay silent.
func TestRegistryAliasToURL_KnownAliasNeverWarns(t *testing.T) {
	root := t.TempDir()
	registerPersonalRemote(t, root)
	reg, err := remote.NewRegistry(paths.RemotesPath(root))
	require.NoError(t, err)

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	got := registryAliasToURL(reg)("personal")
	assert.Equal(t, shortNamePersonalURL, got)
	assert.Empty(t, buf.String(), "resolving a known alias must never warn")
}
