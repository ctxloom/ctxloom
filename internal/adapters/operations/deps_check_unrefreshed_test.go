package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// unrefreshedFixture is a config rooted in a temp dir and a file:// repository
// URL that does not exist, so a refresh of it fails offline.
func unrefreshedFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	return config.NewFixture(config.Fixture{AppDir: appDir, AppPaths: []string{appDir}}),
		"file://" + filepath.Join(root, "no-such-repo")
}

// An entry whose repository could not be fetched this run is UNCHECKED, carrying
// the fetch error — never answered from the clone the last successful fetch
// left behind, which is what made `deps check` print "All items are up to
// date!" right under a fetch warning.
func TestDetectUpdates_UnrefreshedRepositoryIsUnchecked(t *testing.T) {
	cfg, repoURL := unrefreshedFixture(t)
	ref := repoURL + "@bundles/demo"
	lockfile := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{
		trust.BundleKey(ref): {SHA: "abc1234"},
	}}
	fetchErr := errors.New("fetch refused")

	updates, unchecked, _ := detectUpdates(context.Background(), cfg, remote.AuthConfig{}, lockfile,
		[]RefreshFailure{{URL: repoURL, Err: fetchErr}})

	assert.Empty(t, updates)
	require.Len(t, unchecked, 1, "the entry must be reported, not answered")
	assert.Equal(t, UncheckedNotRefreshed, unchecked[0].Reason)
	assert.Equal(t, repoURL, unchecked[0].URL)
	assert.ErrorIs(t, unchecked[0].Err, fetchErr)
}

// The single-reference check has the same obligation: a refresh that failed
// leaves no current answer to give, so it refuses rather than printing a
// status read from a stale clone.
func TestCheckSingleDependency_UnrefreshedRepositoryRefuses(t *testing.T) {
	cfg, repoURL := unrefreshedFixture(t)
	lockManager := remote.NewLockfileManager(ProjectAppDir(cfg))

	res, err := checkSingleDependency(context.Background(), cfg, repoURL+"@bundles/demo", lockManager)

	require.ErrorIs(t, err, ErrCloneNotRefreshed)
	assert.Nil(t, res.Single, "no status may be reported for a repository that was not fetched")
	assert.NotEmpty(t, res.Refresh, "the fetch failure itself is still carried for the caller to show")
}
