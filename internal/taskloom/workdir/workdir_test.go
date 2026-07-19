package workdir

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// runGitInit runs a bare `git init` against dir -- just enough for
// gitutil.FindRoot to see a real repo boundary; no commit, no worktree.
func runGitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", "init", "-q", dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
}

// TestResolve_LinkedWorktreeUsesPrimaryStore is the acceptance case: a task
// added from a linked worktree must land in the SAME project-id/log path as
// one added from the primary checkout, because ResolveBoundary redirects the
// worktree's resolved root through projectroot.TaskStoreRoot.
func TestResolve_LinkedWorktreeUsesPrimaryStore(t *testing.T) {
	home := taskstest.Isolate(t)
	main, linked := taskstest.RealGitWorktreeFixture(t)

	taskstest.ChangeDir(t, main)
	mainRoot, mainFound, err := ResolveBoundary()
	require.NoError(t, err)
	assert.True(t, mainFound)

	taskstest.ChangeDir(t, linked)
	linkedRoot, linkedFound, err := ResolveBoundary()
	require.NoError(t, err)
	assert.True(t, linkedFound)

	assert.Equal(t, mainRoot, linkedRoot, "the linked worktree's resolved root must redirect to the primary checkout")

	// Feed both resolved roots through the SAME identity/log-path machinery
	// `taskloom` itself uses, and confirm they land on one project-id and one
	// log path -- not just equal root strings.
	reg := filepath.Join(home, ".ctxloom-registry", "index.yaml")
	pm, err := projectid.Open(reg)
	require.NoError(t, err)

	mainRes, err := pm.Resolve(mainRoot)
	require.NoError(t, err)
	linkedRes, err := pm.Resolve(linkedRoot)
	require.NoError(t, err)
	assert.Equal(t, mainRes.ProjectID, linkedRes.ProjectID, "same project id from both roots")

	mainLog, err := paths.TasksLogPath(mainRes.ProjectID)
	require.NoError(t, err)
	linkedLog, err := paths.TasksLogPath(linkedRes.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, mainLog, linkedLog, "same task log path from both roots")
}

// TestResolve_WorktreeWithOwnCtxloomStaysSeparate is the opt-out: a linked
// worktree that carries its own .ctxloom (e.g. `ctxloom init` run there) is a
// deliberately separate project and must resolve to ITSELF, not the primary
// checkout.
func TestResolve_WorktreeWithOwnCtxloomStaysSeparate(t *testing.T) {
	taskstest.Isolate(t)
	_, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(linked, ".ctxloom"), 0o755))

	taskstest.ChangeDir(t, linked)
	root, found, err := ResolveBoundary()
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, linked, root, "an opted-out worktree must resolve to itself")
}

// TestResolve_StaleWorktreePointerFailsLoud pins the fail-loud requirement at
// this package's own boundary: a linked worktree whose primary checkout is
// gone must return an error, never silently resolve to the worktree itself
// or to some unrelated ancestor.
func TestResolve_StaleWorktreePointerFailsLoud(t *testing.T) {
	taskstest.Isolate(t)
	main, linked := taskstest.RealGitWorktreeFixture(t)
	require.NoError(t, os.RemoveAll(main))

	taskstest.ChangeDir(t, linked)
	root, _, err := ResolveBoundary()
	assert.Error(t, err)
	assert.Empty(t, root)
}

// TestResolve_PlainGitRepoIsUnaffected covers the ordinary, non-worktree
// case: a plain git repo's root resolves to itself, exactly as before this
// change.
func TestResolve_PlainGitRepoIsUnaffected(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()
	runGitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	root, found, err := ResolveBoundary()
	require.NoError(t, err)
	assert.True(t, found)
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, resolved)
}

// TestResolve_BareCwdFallbackIsUnaffected covers the no-boundary case: no
// CTXLOOM_ROOT, no enclosing git repo -- found is false and the plain cwd is
// returned, exactly as before this change (TaskStoreRoot is a no-op on a
// non-worktree directory).
func TestResolve_BareCwdFallbackIsUnaffected(t *testing.T) {
	dir := taskstest.ProjectDir(t)

	root, found, err := ResolveBoundary()
	require.NoError(t, err)
	assert.False(t, found)
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, resolved)
}
