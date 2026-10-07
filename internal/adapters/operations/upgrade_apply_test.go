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
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// republishKit commits kit again with description desc and returns the commit.
func (p *shippedProfileProject) republishKit(t *testing.T, desc string) string {
	t.Helper()
	authored := authoredV2(filepath.Join(p.repoDir, paths.AppDirName))
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "kit")))
	bundletree.WriteOS(t, authored, "kit", "version: 1.0.0\ndescription: "+desc+"\nprofiles:\n  extra:\n    description: shipped, never composed\n    bundles:\n      - "+p.containRef+"\n")
	repo, err := git.PlainOpen(p.repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit(desc, &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)
	return sha.String()
}

// installedManifest is kit's bundle.yaml as the installed worktree holds it.
func (p *shippedProfileProject) installedManifest(t *testing.T) string {
	t.Helper()
	parsed, err := remote.ParseReference(p.kitRef)
	require.NoError(t, err)
	dir, err := parsed.LocalTreePath(p.appDir)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dir, "bundle.yaml"))
	require.NoError(t, err)
	return string(data)
}

func (p *shippedProfileProject) lockBytes(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(remote.NewLockfileManager(p.appDir).Path())
	require.NoError(t, err)
	return string(data)
}

// Without --yes, an upgrade shows what would move and changes nothing: not
// the lockfile, not the installed tree, not the refusal record.
func TestUpgrade_PreviewWritesNothingAndMovesNoWorktree(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	before, _ := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	lockBefore, treeBefore := p.lockBytes(t), p.installedManifest(t)
	next := p.republishKit(t, "kit, second edition")

	res, err := UpgradeDependencies(context.Background(), p.cfg(t), UpgradeRequest{})
	require.NoError(t, err)

	assert.False(t, res.Applied)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, before.SHA, res.Changes[0].FromSHA)
	assert.Equal(t, next, res.Changes[0].ToSHA)
	assert.Equal(t, lockBefore, p.lockBytes(t), "a preview never writes the lockfile")
	assert.Equal(t, treeBefore, p.installedManifest(t), "a preview never moves the installed tree")
}

// --yes recomputes at apply time and reports exactly what it applied: a tip
// that moved after the preview is what lands, and what is shown.
func TestUpgrade_YesAppliesWhatItReports(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	p.republishKit(t, "kit, second edition")
	preview, err := UpgradeDependencies(context.Background(), p.cfg(t), UpgradeRequest{})
	require.NoError(t, err)
	newest := p.republishKit(t, "kit, third edition")
	require.NotEqual(t, preview.Changes[0].ToSHA, newest)

	res, err := UpgradeDependencies(context.Background(), p.cfg(t), UpgradeRequest{Apply: true})
	require.NoError(t, err)

	assert.True(t, res.Applied)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, newest, res.Changes[0].ToSHA, "the applied change is the one recomputed at apply time")
	after, _ := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	assert.Equal(t, newest, after.SHA)
	assert.Contains(t, p.installedManifest(t), "third edition", "the moved pin moves the tree")
}
