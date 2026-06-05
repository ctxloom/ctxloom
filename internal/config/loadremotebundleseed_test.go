package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedSourceRepo creates a real git repo shipping a valid bundle and a
// malformed one, returning its path and HEAD SHA. The full-load path of
// loadRemoteBundleSeed clones this over file:// and reads each locked bundle at
// that SHA.
func seedSourceRepo(t *testing.T) (repoDir, sha string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(repoDir, "ctxloom", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "good.yaml"),
		[]byte("version: v1\ndescription: a good bundle\n"), 0644))
	// A leading tab is invalid YAML, so ParseBundle rejects this one.
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "bad.yaml"),
		[]byte("\tnot: valid yaml\n"), 0644))

	for _, f := range []string{"ctxloom/bundles/good.yaml", "ctxloom/bundles/bad.yaml"} {
		_, err = wt.Add(f)
		require.NoError(t, err)
	}
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return repoDir, commit.String()
}

// TestLoadRemoteBundleSeed_FullLoad covers the materialization path past the
// guard branches: a non-empty lockfile drives a real clone of the locked repo,
// each bundle is read at its SHA and parsed, a malformed bundle is skipped, and
// each loaded bundle carries its name and synthetic "<remote>:name@sha" path.
func TestLoadRemoteBundleSeed_FullLoad(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha := seedSourceRepo(t)
	repoURL := "file://" + repoDir

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry := remote.LockEntry{SHA: sha, URL: repoURL, FetchedAt: time.Now().UTC()}
	lock.AddEntry(remote.ItemTypeBundle, "src/good", entry)
	lock.AddEntry(remote.ItemTypeBundle, "src/bad", entry)
	require.NoError(t, lm.Save(lock))

	cfg := &Config{AppPaths: []string{appDir}}
	seed := cfg.loadRemoteBundleSeed()

	require.NotNil(t, seed, "a populated lockfile must materialize a seed")
	good, ok := seed["src/good"]
	require.True(t, ok, "the valid bundle is loaded from the clone")
	assert.Equal(t, "src/good", good.Name)
	assert.Equal(t, "v1", good.Version, "bundle content is parsed at the locked SHA")
	assert.Equal(t, "<remote>:src/good@"+sha, good.Path)

	_, badLoaded := seed["src/bad"]
	assert.False(t, badLoaded, "a malformed bundle is skipped, not fatal")
}
