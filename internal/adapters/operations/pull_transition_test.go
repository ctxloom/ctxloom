package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// commitAll stages every change under dir (deletions included) and commits it,
// returning the new commit.
func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)
	return sha.String()
}

// rolesProfile is the `roles` bundle shipping a profile `finder` that composes
// one same-repository bundle by its short name.
func rolesProfile(bundle string) string {
	return "version: 1.0.0\ndescription: roles\nprofiles:\n  finder:\n    description: finder\n    bundles:\n      - " + bundle + "\n"
}

// leafBundle is a bundle carrying one fragment: a bundle with no items is
// refused as empty.
func leafBundle(name string) string {
	return "version: 1.0.0\ndescription: " + name + "\nfragments:\n  " + name + ":\n    content: " + name + " body\n"
}

// A bundle moves out of a repository: upstream deletes it and the shipped
// profile that composed it stops naming it, and the project's lock — regenerated
// elsewhere and arriving through git — no longer pins it. The installed tree of
// the profile's bundle is still at the OLD commit, where the profile does name
// it. The first pull must install the new lock's closure, not fetch the departed
// bundle at the new commit, where it does not exist.
func TestPull_BundleDroppedByAMovedPinIsNotFetched(t *testing.T) {
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repoURL := "file://" + repoDir
	rolesRef := repoURL + "@bundles/roles"
	toolRef := repoURL + "@bundles/tool"
	keepRef := repoURL + "@bundles/keep"

	_, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	authored := authoredV2(filepath.Join(repoDir, paths.AppDirName))
	require.NoError(t, os.MkdirAll(authored, 0o755))
	bundletree.WriteOS(t, authored, "roles", rolesProfile("tool"))
	bundletree.WriteOS(t, authored, "tool", leafBundle("tool"))
	bundletree.WriteOS(t, authored, "keep", leafBundle("keep"))
	commitAll(t, repoDir, "seed")

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	registerTestRemote(t, appDir, repoURL)
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "dev.yaml"),
		[]byte("parents:\n  - "+rolesRef+"#profiles/finder\n"), 0o644))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir),
		[]byte("schema_version: 7\ndefault_agent: default\nagents:\n  default:\n    profiles: [dev]\n"), 0o644))
	app := pulledApp(t, appDir)
	ctx := context.Background()

	first, err := SyncDependencies(ctx, app, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)
	require.Empty(t, first.Failed)
	lm := remote.NewLockfileManager(appDir)
	lf, err := lm.Load()
	require.NoError(t, err)
	_, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, toolRef))
	require.True(t, ok, "the old closure pins tool")

	// Upstream: tool leaves the repository and finder composes keep instead.
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "tool")))
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "roles")))
	bundletree.WriteOS(t, authored, "roles", rolesProfile("keep"))
	newPin := commitAll(t, repoDir, "move tool out")

	// The new lock, as `deps lock` wrote it elsewhere: roles at the new commit,
	// keep pinned, tool gone. Nothing in the cache has moved.
	rolesEntry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, rolesRef))
	require.True(t, ok)
	rolesEntry.SHA = newPin
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, rolesRef), rolesEntry)
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, keepRef), rolesEntry)
	lf.RemoveEntry(remote.ItemTypeBundle, lockKeyOf(t, toolRef))
	require.NoError(t, lm.Save(lf))
	_, err = app.Reload(ctx)
	require.NoError(t, err)

	res, err := SyncDependencies(ctx, app, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)

	var touched []string
	for _, items := range [][]SyncItem{res.Synced, res.Skipped, res.Failed} {
		for _, it := range items {
			touched = append(touched, string(lockKeyOf(t, it.Reference)))
		}
	}
	assert.Empty(t, res.Failed, "the first pull after the lock moved succeeds")
	assert.NotContains(t, touched, string(lockKeyOf(t, toolRef)), "a bundle the new lock dropped is never fetched")

	after, err := lm.Load()
	require.NoError(t, err)
	_, ok = after.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, toolRef))
	assert.False(t, ok, "the dropped bundle stays out of the lock")
	_, ok = after.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, keepRef))
	assert.True(t, ok, "the new closure stays pinned")
	got, ok := after.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, rolesRef))
	require.True(t, ok)
	assert.Equal(t, newPin, got.SHA, "pull never moves the pin")
}
