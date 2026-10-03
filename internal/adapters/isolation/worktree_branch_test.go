package isolation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
)

// A torn-down checkout's branch is deleted once it is merged into the repo's
// current branch: it then holds nothing the repo does not, and left behind it
// only accumulates. Deleted with git's safe delete, never a forced one.
func TestTeardownWorktree_DeletesTheMergedBranch(t *testing.T) {
	target := filepath.Join(t.TempDir(), worktreeScratchPrefix+"-member-a")
	branch := worktreeBranchName(target)
	f := &git.Fake{MergedBranchesValue: []string{"main", branch}}

	teardownWorktree(context.Background(), f, "/repo", target)

	assert.Equal(t, []string{target}, f.Removed)
	assert.Equal(t, []string{branch}, f.DeletedBranches)
}

// An unmerged branch holds commits nothing else has: it is kept.
func TestTeardownWorktree_KeepsAnUnmergedBranch(t *testing.T) {
	target := filepath.Join(t.TempDir(), worktreeScratchPrefix+"-member-b")
	f := &git.Fake{MergedBranchesValue: []string{"main"}}

	teardownWorktree(context.Background(), f, "/repo", target)

	assert.Equal(t, []string{target}, f.Removed)
	assert.Empty(t, f.DeletedBranches)
}

// A checkout that is not removed (it holds work) keeps its branch whatever
// its merge state.
func TestTeardownWorktree_KeepsTheBranchOfAPreservedCheckout(t *testing.T) {
	target := filepath.Join(t.TempDir(), worktreeScratchPrefix+"-member-c")
	branch := worktreeBranchName(target)
	f := &git.Fake{MergedBranchesValue: []string{branch}, Dirty: map[string]bool{target: true}}

	teardownWorktree(context.Background(), f, "/repo", target)

	assert.Empty(t, f.Removed)
	assert.Empty(t, f.DeletedBranches)
}
