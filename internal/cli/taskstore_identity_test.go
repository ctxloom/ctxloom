package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestTaskStoreWorkDir_RedirectsLinkedWorktree covers `ctxloom run`'s
// CTXLOOM_PROJECT_ID export path: a linked git worktree with no .ctxloom of
// its own redirects to its primary checkout, exactly like
// internal/taskloom/workdir's own redirect.
func TestTaskStoreWorkDir_RedirectsLinkedWorktree(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)

	got := taskStoreWorkDir(linked)
	assert.Equal(t, main, got)
}

// TestTaskStoreWorkDir_OwnCtxloomStaysSeparate covers the opt-out: a
// worktree carrying its own .ctxloom is a deliberately separate project.
func TestTaskStoreWorkDir_OwnCtxloomStaysSeparate(t *testing.T) {
	_, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(linked, ".ctxloom"), 0o755))

	got := taskStoreWorkDir(linked)
	assert.Equal(t, linked, got)
}

// TestTaskStoreWorkDir_StalePointerWarnsAndFallsBack covers this call site's
// fault-tolerance stance: a stale worktree pointer must never block `ctxloom
// run`'s CTXLOOM_PROJECT_ID export -- it warns (see clidiag) and falls back
// to workDir UNCHANGED (today's worktree-distinct id), rather than
// propagating the TaskStoreRoot error up and leaving the session with no
// project id at all.
func TestTaskStoreWorkDir_StalePointerWarnsAndFallsBack(t *testing.T) {
	main, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.RemoveAll(main))

	got := taskStoreWorkDir(linked)
	assert.Equal(t, linked, got, "a stale pointer must fall back to workDir unchanged, not block")
}

// TestTaskStoreWorkDir_PlainDirectoryIsUnaffected covers the common,
// non-worktree case: an ordinary directory is returned unchanged.
func TestTaskStoreWorkDir_PlainDirectoryIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	got := taskStoreWorkDir(dir)
	assert.Equal(t, dir, got)
}
