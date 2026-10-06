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
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// unregisteredSourceProject is a project whose profile names a bundle of a
// repository no remote is registered for.
func unregisteredSourceProject(t *testing.T) (baseDir, srcURL string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src := filepath.Join(tmp, "src")
	initLocalRepoWithFile(t, src, repoV2("demo")+"/bundle.yaml", "version: \"1.0.0\"\n")
	srcURL = "file://" + src
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+srcURL+"@bundles/demo\n")
	return baseDir, srcURL
}

// assertRefusedBeforeAnyFetch checks the registration refusal, and that it
// came before anything was cloned or pinned.
func assertRefusedBeforeAnyFetch(t *testing.T, err error, baseDir, srcURL string) {
	t.Helper()
	require.ErrorIs(t, err, remote.ErrRemoteNotRegistered)
	var rem report.Remediable
	require.True(t, errors.As(err, &rem), "the refusal carries its fix")
	assert.Equal(t, "ctxloom remote create <name> "+srcURL, rem.Remedy())

	entries, rerr := os.ReadDir(paths.ReposCachePath(baseDir))
	if rerr == nil {
		assert.Empty(t, entries, "nothing was cloned")
	} else {
		assert.True(t, os.IsNotExist(rerr), "%v", rerr)
	}
	_, serr := os.Stat(paths.LockPath(baseDir))
	assert.True(t, os.IsNotExist(serr), "no lock entry was written")
}

// `deps lock` over an unregistered repository is refused before the walk
// fetches it.
func TestLockDependencies_UnregisteredRepositoryIsRefusedBeforeAnyFetch(t *testing.T) {
	baseDir, srcURL := unregisteredSourceProject(t)
	_, err := LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{FailOnConflict: true})
	assertRefusedBeforeAnyFetch(t, err, baseDir, srcURL)
}

// Upgrade's re-resolve, including its pre-walk clone refresh, meets the same
// refusal.
func TestUpgradeDependencies_UnregisteredRepositoryIsRefusedBeforeAnyFetch(t *testing.T) {
	baseDir, srcURL := unregisteredSourceProject(t)
	_, err := UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir), nil)
	assertRefusedBeforeAnyFetch(t, err, baseDir, srcURL)
}
