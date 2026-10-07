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
	"github.com/ctxloom/ctxloom/internal/core/config"
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

// composeFinder is how a project reaches roles#profiles/finder.
type composeFinder int

const (
	// viaLocalParent: a project profile names it as a parent.
	viaLocalParent composeFinder = iota
	// viaConfigDefault: the default agent names it directly
	// (configDefaultsRoot).
	viaConfigDefault
)

func (c composeFinder) String() string {
	if c == viaConfigDefault {
		return "config default"
	}
	return "local parent"
}

// movedPinProject is a project mid-way through a bundle leaving a repository.
// At the old commit `roles#profiles/finder` composes `tool`; at newPin `tool` is
// deleted and finder composes `keep`. The lock — regenerated elsewhere and
// arriving through git — pins roles and keep at newPin and has dropped tool,
// but the installed roles tree is still at the old commit, where finder still
// names tool.
type movedPinProject struct {
	app                        *App
	appDir                     string
	rolesRef, toolRef, keepRef string
	newPin                     string
}

func newMovedPinProject(t *testing.T, compose composeFinder) *movedPinProject {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repoURL := "file://" + repoDir
	p := &movedPinProject{
		rolesRef: repoURL + "@bundles/roles",
		toolRef:  repoURL + "@bundles/tool",
		keepRef:  repoURL + "@bundles/keep",
	}
	finderRef := p.rolesRef + "#profiles/finder"

	_, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	authored := authoredV2(filepath.Join(repoDir, paths.AppDirName))
	require.NoError(t, os.MkdirAll(authored, 0o755))
	bundletree.WriteOS(t, authored, "roles", rolesProfile("tool"))
	bundletree.WriteOS(t, authored, "tool", leafBundle("tool"))
	bundletree.WriteOS(t, authored, "keep", leafBundle("keep"))
	oldPin := commitAll(t, repoDir, "seed")

	p.appDir = filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, p.appDir), 0o755))
	registerTestRemote(t, p.appDir, repoURL)
	defaults := "[dev]"
	if compose == viaConfigDefault {
		defaults = "[" + finderRef + "]"
	} else {
		require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, p.appDir), "dev.yaml"),
			[]byte("parents:\n  - "+finderRef+"\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(paths.ConfigPath(p.appDir),
		[]byte("schema_version: 7\ndefault_agent: default\nagents:\n  default:\n    profiles: "+defaults+"\n"), 0o644))
	p.app = pulledApp(t, p.appDir)
	ctx := context.Background()

	first, err := SyncDependencies(ctx, p.app, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)
	require.Empty(t, first.Failed)
	lm := remote.NewLockfileManager(p.appDir)
	lf, err := lm.Load()
	require.NoError(t, err)
	_, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.toolRef))
	require.True(t, ok, "the old closure pins tool")

	// Upstream: tool leaves the repository and finder composes keep instead.
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "tool")))
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "roles")))
	bundletree.WriteOS(t, authored, "roles", rolesProfile("keep"))
	p.newPin = commitAll(t, repoDir, "move tool out")

	// The new lock, as `deps lock` wrote it elsewhere: roles at the new commit,
	// keep pinned, tool gone. Nothing in the cache has moved.
	rolesEntry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.rolesRef))
	require.True(t, ok)
	rolesEntry.SHA = p.newPin
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, p.rolesRef), rolesEntry)
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, p.keepRef), rolesEntry)
	lf.RemoveEntry(remote.ItemTypeBundle, lockKeyOf(t, p.toolRef))
	require.NoError(t, lm.Save(lf))
	require.Equal(t, oldPin, p.rolesTreeCommit(t), "the installed roles tree is still at the old commit")
	return p
}

// cfg reloads the project and returns the configuration it now reads.
func (p *movedPinProject) cfg(t *testing.T) *config.Config {
	t.Helper()
	snap, err := p.app.Reload(context.Background())
	require.NoError(t, err)
	return snap.Config
}

// refreshClone advances the project's clone of the source to its tip, as a
// fetch on any earlier command would have.
func (p *movedPinProject) refreshClone(t *testing.T) {
	t.Helper()
	cfg := p.cfg(t)
	registered, err := registeredRepos(cfg)
	require.NoError(t, err)
	parsed, err := remote.ParseReference(p.rolesRef)
	require.NoError(t, err)
	require.False(t, refreshRepoCaches(context.Background(), NewRepoCache(cfg), []string{parsed.URL}, registered))
}

func (p *movedPinProject) rolesTreeCommit(t *testing.T) string {
	t.Helper()
	parsed, err := remote.ParseReference(p.rolesRef)
	require.NoError(t, err)
	worktree, err := parsed.LocalWorktreePath(p.appDir)
	require.NoError(t, err)
	head, err := remote.WorktreeCommit(context.Background(), worktree)
	require.NoError(t, err)
	return head
}

func (p *movedPinProject) lockHas(t *testing.T, ref string) bool {
	t.Helper()
	lf, err := remote.NewLockfileManager(p.appDir).Load()
	require.NoError(t, err)
	_, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, ref))
	return ok
}

func bothCompositions(t *testing.T, run func(t *testing.T, compose composeFinder)) {
	for _, c := range []composeFinder{viaLocalParent, viaConfigDefault} {
		t.Run(c.String(), func(t *testing.T) { run(t, c) })
	}
}

// The first pull after the lock moved installs the new lock's closure, rather
// than fetching the departed bundle at the new commit, where it does not exist.
func TestPull_BundleDroppedByAMovedPinIsNotFetched(t *testing.T) {
	bothCompositions(t, func(t *testing.T, compose composeFinder) {
		p := newMovedPinProject(t, compose)
		p.cfg(t)

		res, err := SyncDependencies(context.Background(), p.app, SyncDependenciesRequest{Lock: true})
		require.NoError(t, err)

		var touched []string
		for _, items := range [][]SyncItem{res.Synced, res.Skipped, res.Failed} {
			for _, it := range items {
				touched = append(touched, string(lockKeyOf(t, it.Reference)))
			}
		}
		assert.Empty(t, res.Failed, "the first pull after the lock moved succeeds")
		assert.NotContains(t, touched, string(lockKeyOf(t, p.toolRef)), "a bundle the new lock dropped is never fetched")
		assert.False(t, p.lockHas(t, p.toolRef), "the dropped bundle stays out of the lock")
		assert.True(t, p.lockHas(t, p.keepRef), "the new closure stays pinned")
		assert.Equal(t, p.newPin, p.rolesTreeCommit(t), "the parent is reinstalled at its pin")
	})
}

// The lock-side walk reads a bundle profile out of the CLONE at the resolved
// commit, never out of the installed tree, so a tree left at the old commit
// cannot put the dropped bundle back. keep present and nothing unexpanded is
// what proves the walk really descended into finder at the new commit, rather
// than passing because it skipped the subtree.
func TestFlattenDependencies_TreeOffItsPinDoesNotRepinADroppedBundle(t *testing.T) {
	bothCompositions(t, func(t *testing.T, compose composeFinder) {
		p := newMovedPinProject(t, compose)
		p.refreshClone(t)

		pins, conflicts, unexpanded, err := FlattenDependencies(context.Background(), p.cfg(t), nil)
		require.NoError(t, err)
		assert.Empty(t, conflicts)
		assert.Empty(t, unexpanded, "finder was read at the new commit")
		ids := pinIdentities(pins)
		assert.NotContains(t, ids, string(lockKeyOf(t, p.toolRef)), "the dropped bundle is not pinned again")
		assert.Contains(t, ids, string(lockKeyOf(t, p.keepRef)))
		assert.NotEqual(t, p.newPin, p.rolesTreeCommit(t), "the walk ran against the stale tree")
	})
}

// `deps lock` rewrites the lock from that walk: the dropped bundle stays out.
func TestLockDependencies_TreeOffItsPinDoesNotRepinADroppedBundle(t *testing.T) {
	bothCompositions(t, func(t *testing.T, compose composeFinder) {
		p := newMovedPinProject(t, compose)
		p.refreshClone(t)

		res, err := LockDependencies(context.Background(), p.cfg(t), LockDependenciesRequest{})
		require.NoError(t, err)
		assert.False(t, res.Incomplete, "finder was read at the new commit")
		assert.False(t, p.lockHas(t, p.toolRef), "the dropped bundle is not pinned again")
		assert.True(t, p.lockHas(t, p.keepRef))
	})
}

// `deps upgrade` — what runs in a checkout holding the stale trees right after
// the bundle repositories merge — advances to the tip without re-pinning the
// dropped bundle or fetching it.
func TestUpgradeDependencies_TreeOffItsPinDoesNotRepinADroppedBundle(t *testing.T) {
	bothCompositions(t, func(t *testing.T, compose composeFinder) {
		p := newMovedPinProject(t, compose)

		res, err := UpgradeDependencies(context.Background(), p.cfg(t), UpgradeRequest{Apply: true})
		require.NoError(t, err)
		assert.False(t, res.Incomplete, "finder was read at the new commit")
		assert.False(t, p.lockHas(t, p.toolRef), "the dropped bundle is not pinned again")
		assert.True(t, p.lockHas(t, p.keepRef))
	})
}
