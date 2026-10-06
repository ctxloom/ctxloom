package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// Content already installed from a repository that is no longer registered
// stops resolving: its lockfile entry is withheld as a failure naming the
// fix, while an entry of a registered repository still reads.
func TestRegisteredEntries_WithholdsAnUnregisteredRepository(t *testing.T) {
	const orphan = "ctxloom+git://github.com/gone/away//bundles/kit"
	c, _, _, fsys := stageInstalledTree(t)
	reg, err := remote.NewRegistry(paths.RemotesPath(treeBase), remote.WithRegistryFS(fsys))
	require.NoError(t, err)
	require.NoError(t, reg.Add("acme", "https://github.com/acme/ctx"))

	lock := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{
		treeCanonical: treeEntry(),
		orphan:        {SHA: "0123456789abcdef", URL: "https://github.com/gone/away"},
	}}
	failures := map[trust.BundleKey]error{}

	kept := registeredEntries(lock, reg, failures)
	assert.Contains(t, kept.Bundles, trust.BundleKey(treeCanonical))
	assert.NotContains(t, kept.Bundles, trust.BundleKey(orphan))
	assert.Contains(t, lock.Bundles, trust.BundleKey(orphan), "the lockfile itself is not edited")
	require.ErrorIs(t, failures[orphan], remote.ErrRemoteNotRegistered)

	readers := pinnedTreeReaders(c, kept, trust.NoSigners{}, failures)
	reads := bundles.NewLoader(readers...).Reads()
	names := make([]string, 0, len(reads))
	for _, r := range reads {
		names = append(names, r.DisplayName())
	}
	assert.Equal(t, []string{treeCanonical}, names, "only the registered repository's content resolves")
	assert.ErrorIs(t, failures[orphan], remote.ErrRemoteNotRegistered, "the withheld entry stays a reported failure")
}

// A pinned historical version of a bundle from an unregistered repository is
// refused before the clone cache is touched.
func TestBundleVersionResolver_RefusesAnUnregisteredRepository(t *testing.T) {
	fsys := afero.NewMemMapFs()
	c := gatedFixture(config.Fixture{AppPaths: []string{treeBase}})
	c.SetFS(fsys)

	resolve := BundleVersionResolver(c)
	require.NotNil(t, resolve)
	_, err := resolve("https://github.com/gone/away@bundles/kit", "0123456789abcdef", trust.NoSigners{})
	require.ErrorIs(t, err, remote.ErrRemoteNotRegistered)
}

// registerTestRemote registers repoURL as a remote of the project at appDir,
// on the OS filesystem — the `ctxloom remote create` a fixture pulling from
// that repository needs, since nothing registers a remote implicitly.
func registerTestRemote(t *testing.T, appDir, repoURL string) {
	t.Helper()
	reg, err := remote.NewRegistry(paths.RemotesPath(appDir))
	require.NoError(t, err)
	require.NoError(t, reg.Add("source", repoURL))
}
