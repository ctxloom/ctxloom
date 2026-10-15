package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBranchExists_ReportsWhatTheRepoHas: a branch the repository has is
// reported, one it lacks is not, a probe git refuses is an error, and an
// unnamed branch is refused before git runs — `git branch --list ""` would
// list every branch and read as "exists".
func TestBranchExists_ReportsWhatTheRepoHas(t *testing.T) {
	dir := initRepo(t)
	gitIn(t, dir, "branch", "present")
	g, ctx := NewExec(), context.Background()

	exists, err := g.BranchExists(ctx, dir, MustParseBranch("present"))
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = g.BranchExists(ctx, dir, MustParseBranch("absent"))
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = g.BranchExists(ctx, t.TempDir(), MustParseBranch("present"))
	require.Error(t, err, "outside a repository there is no answer")

	_, err = g.BranchExists(ctx, dir, Branch{})
	require.Error(t, err)
}

// TestWorktreeAdd_NeverDetached: a per-agent checkout is always on a branch;
// with none named, nothing is created.
func TestWorktreeAdd_NeverDetached(t *testing.T) {
	dir := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	require.Error(t, NewExec().WorktreeAdd(context.Background(), dir, path, Branch{}, "HEAD", false))
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "no checkout was made")
}

// TestClone_ChecksOutTheNamedBranch: a clone pinned to a branch is on that
// branch, not the remote's default.
func TestClone_ChecksOutTheNamedBranch(t *testing.T) {
	bare := bareWithSeed(t, "main", "seed.txt")
	work := filepath.Join(t.TempDir(), "seed")
	runGit(t, filepath.Dir(work), "clone", "file://"+bare, work)
	runGit(t, work, "switch", "-c", "feature")
	runGit(t, work, "push", "origin", "feature")

	clone := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, NewExec().Clone(context.Background(), "file://"+bare, clone, MustParseBranch("feature")))
	assert.Equal(t, "feature", runGit(t, clone, "branch", "--show-current"))
}

// TestFake_CurrentBranchDefaultsToMain: the fake's documented default — no
// value set, no error injected, not detached — is main.
func TestFake_CurrentBranchDefaultsToMain(t *testing.T) {
	b, err := (&Fake{}).CurrentBranch(context.Background(), "/repo")
	require.NoError(t, err)
	assert.Equal(t, MustParseBranch("main"), b)
}
