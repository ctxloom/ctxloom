package projectroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestTaskStoreRoot_LinkedWorktreeUsesPrimaryStore is the acceptance case for
// this whole seam: a linked worktree with no .ctxloom of its own resolves to
// its PRIMARY checkout root, not its own — so a task filed from the worktree
// lands in the log the coordinator (running from the primary checkout)
// actually reads.
func TestTaskStoreRoot_LinkedWorktreeUsesPrimaryStore(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)

	got, err := TaskStoreRoot(afero.NewOsFs(), linked)
	require.NoError(t, err)
	assert.Equal(t, main, got, "a linked worktree with no .ctxloom of its own must redirect to the primary checkout")

	// The main worktree itself is always its own task-store root.
	gotMain, err := TaskStoreRoot(afero.NewOsFs(), main)
	require.NoError(t, err)
	assert.Equal(t, main, gotMain)
}

// TestTaskStoreRoot_WorktreeWithOwnCtxloomStaysSeparate is the opt-out: a
// worktree that carries its own .ctxloom (an explicit `ctxloom init` there)
// is a deliberately separate project and must NOT be redirected — mirroring
// worktreeSignpost's own precedence rule in internal/config/config.go.
func TestTaskStoreRoot_WorktreeWithOwnCtxloomStaysSeparate(t *testing.T) {
	_, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(linked, ".ctxloom"), 0o755))

	got, err := TaskStoreRoot(afero.NewOsFs(), linked)
	require.NoError(t, err)
	assert.Equal(t, linked, got, "an opted-out worktree (own .ctxloom) must never redirect")
}

// TestDetectWorktree_StaleMainRootFailsLoud pins the fail-loud requirement: a
// linked worktree whose gitdir pointer names a primary checkout that no
// longer exists must surface a hard error, never a silent fallback to the
// worktree's own (orphaned) task store.
func TestDetectWorktree_StaleMainRootFailsLoud(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.RemoveAll(main))

	got, err := TaskStoreRoot(afero.NewOsFs(), linked)
	require.Error(t, err, "a stale worktree pointer must fail loud, not silently resolve to the worktree itself")
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), main, "the error names the missing primary checkout")
}

// TestTaskStoreRoot_NonWorktreeDirectoryIsUnaffected covers the two
// no-op shapes: a plain, non-git directory, and the main worktree of a
// repo with no linked worktrees at all — neither is ever redirected.
func TestTaskStoreRoot_NonWorktreeDirectoryIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	got, err := TaskStoreRoot(afero.NewOsFs(), dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

// TestTaskStoreRoot_UnreadableGitFileSurfacesAsError mirrors
// DetectWorktree's own "unreadable .git file" case: TaskStoreRoot must
// surface it, not silently treat the directory as a plain non-worktree.
func TestTaskStoreRoot_UnreadableGitFileSurfacesAsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores file permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".git")
	require.NoError(t, os.WriteFile(p, []byte("gitdir: /x/.git/worktrees/y\n"), 0o644))
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	_, err := TaskStoreRoot(afero.NewOsFs(), dir)
	assert.Error(t, err)
}
