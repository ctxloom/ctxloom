package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// treeFailure runs treeBundleReader over a project whose cache holds only what
// stage lays down, and returns the error and the finding it is reported as.
func treeFailure(t *testing.T, entry remote.LockEntry, stage func(fsys afero.Fs)) (text, fix string, err error) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	stage(fsys)
	c := config.NewFixture(config.Fixture{AppPaths: []string{treeBase}})
	c.SetFS(fsys)
	_, err = treeBundleReader(c, treeCanonical, entry, nil)
	require.Error(t, err)
	f := withheldFinding(t, err)
	return f.Text, f.Remedy, err
}

// The fix line must be one that repairs what failed. A pin that was never
// pulled is repaired by `deps pull`. A pin whose commit lacks the bundle (it
// predates a repository layout move) is NOT: pull keeps an existing pin at its
// commit, so the repair is the command that advances it.
func TestBundleLoadFailure_FixLineRepairsTheCause(t *testing.T) {
	ref, err := remote.ParseReference(treeCanonical)
	require.NoError(t, err)
	worktree, err := ref.LocalWorktreePath(treeBase)
	require.NoError(t, err)

	t.Run("never pulled", func(t *testing.T) {
		text, fix, err := treeFailure(t, treeEntry(), func(afero.Fs) {})
		assert.ErrorIs(t, err, ErrTreeNotInstalled)
		assert.Contains(t, text, "not installed")
		assert.Contains(t, fix, "ctxloom deps pull")
		assert.NotContains(t, fix, "deps upgrade")
	})

	installedWithoutBundle := func(fsys afero.Fs) { require.NoError(t, fsys.MkdirAll(worktree, 0o755)) }

	t.Run("absent at the pinned commit", func(t *testing.T) {
		entry := treeEntry()
		text, fix, err := treeFailure(t, entry, installedWithoutBundle)
		assert.ErrorIs(t, err, ErrTreeAbsentAtPin)
		assert.NotErrorIs(t, err, ErrTreeNotInstalled)
		assert.Contains(t, text, entry.SHA)
		assert.Contains(t, fix, "ctxloom deps upgrade")
		assert.NotContains(t, fix, "deps pull", "pull keeps the pin; it cannot repair this")
		assert.NotContains(t, fix, "unhold")
	})

	t.Run("absent at a held pin", func(t *testing.T) {
		entry := treeEntry()
		entry.Held = true
		_, fix, err := treeFailure(t, entry, installedWithoutBundle)
		assert.ErrorIs(t, err, ErrTreeAbsentAtPin)
		assert.Contains(t, fix, "ctxloom deps unhold "+treeCanonical, "upgrade skips a held pin")
		assert.Contains(t, fix, "ctxloom deps upgrade")
	})
}
