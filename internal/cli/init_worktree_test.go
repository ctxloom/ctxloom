package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestResolveAppDir_LinkedWorktreeGetsItsOwnAppDir is the regression test for
// `ctxloom init`'s documented opt-out (see internal/projectroot.TaskStoreRoot
// and taskstore_identity_test.go's OwnCtxloomStaysSeparate case): running
// `ctxloom init` FROM a linked git worktree must create .ctxloom under the
// worktree itself, never redirected to the primary checkout, or the opt-out
// this whole seam depends on could never be exercised in the first place.
//
// resolveAppDir is plain os.Getwd()-based with no worktree awareness at all,
// so this was previously true by construction rather than by test -- this
// pins it down rather than leaving it assumed.
func TestResolveAppDir_LinkedWorktreeGetsItsOwnAppDir(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)
	t.Chdir(linked)

	got, err := resolveAppDir(false)
	require.NoError(t, err)

	wantLinked := filepath.Join(linked, ".ctxloom")
	wantMain := filepath.Join(main, ".ctxloom")
	assert.Equal(t, wantLinked, got, "resolveAppDir must target the worktree's own directory")
	assert.NotEqual(t, wantMain, got, "resolveAppDir must NOT redirect a linked worktree to the primary checkout")
}

// TestResolveAppDir_LinkedWorktreeInitIsFullyIndependent goes one step
// further than resolveAppDir alone: it exercises the composed behavior a
// user actually gets from `ctxloom init` in a linked worktree -- an app dir
// created there, then the SAME opt-out that taskStoreWorkDir honors (own
// .ctxloom => deliberately separate project), proving the two seams compose
// into one fully independent project rather than one silently overriding
// the other.
func TestResolveAppDir_LinkedWorktreeInitIsFullyIndependent(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)
	t.Chdir(linked)

	appDir, err := resolveAppDir(false)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	// The primary checkout must be untouched by this "init".
	_, statErr := os.Stat(filepath.Join(main, ".ctxloom"))
	assert.True(t, os.IsNotExist(statErr), "init in the linked worktree must not create .ctxloom in the primary checkout")

	// And the task-store seam must now treat the worktree as independent,
	// not redirect it back to main.
	got := taskStoreWorkDir(linked)
	assert.Equal(t, linked, got, "a worktree with its own .ctxloom must resolve task-store identity to itself, fully independent of the primary checkout")
}
