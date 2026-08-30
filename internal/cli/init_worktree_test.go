package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestResolveAppDir_LinkedWorktreeGetsItsOwnAppDir is the regression test for
// `ctxloom init`'s documented opt-out (see internal/projectroot.TaskStoreRoot
// and taskstore_identity_test.go's OwnProjectIDStaysSeparate case): running
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
// created there, identity established there, and then the SAME opt-out that
// taskStoreWorkDir honors, proving the seams compose into one fully
// independent project rather than one silently overriding the other.
//
// establishProjectIdentity is the load-bearing step and the reason this is
// not just a mkdir. The opt-out keys on the project-id MARKER, which is
// gitignored and therefore never arrives by checkout; if `ctxloom init`
// stopped minting one, its own documented remedy ("run `ctxloom init` here
// to make this worktree deliberately separate") would silently do nothing
// and the worktree would keep resolving to the primary checkout.
func TestResolveAppDir_LinkedWorktreeInitIsFullyIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	main, linked := taskstest.RealGitWorktreeFixture(t)
	t.Chdir(linked)

	appDir, err := resolveAppDir(false)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, establishProjectIdentity(linked))

	// The primary checkout must be untouched by this "init".
	_, statErr := os.Stat(filepath.Join(main, ".ctxloom"))
	assert.True(t, os.IsNotExist(statErr), "init in the linked worktree must not create .ctxloom in the primary checkout")

	// establishProjectIdentity must actually have written the marker; the
	// opt-out below is worthless if the mint was a silent no-op.
	marker, readErr := os.ReadFile(filepath.Join(linked, ".ctxloom", "project-id"))
	require.NoError(t, readErr, "`ctxloom init` must mint a project-id marker, or its own remedy does nothing")
	assert.NotEmpty(t, strings.TrimSpace(string(marker)), "a blank marker is no marker at all")

	// And the task-store seam must now treat the worktree as independent,
	// not redirect it back to main.
	got := taskStoreWorkDir(linked)
	assert.Equal(t, linked, got, "a worktree that `ctxloom init` has given its own identity must resolve task-store identity to itself, fully independent of the primary checkout")
}
