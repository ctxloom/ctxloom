package projectroot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// brokenRepoDir builds a directory whose .git exists but is unusable: the
// linked-worktree/submodule form (.git as a FILE) carrying content that is not
// a gitdir pointer. Measured against go-git: this is one of the few shapes
// that fails with something OTHER than "repository does not exist" — a .git
// DIRECTORY with a corrupt HEAD still opens fine, and a dangling `gitdir:`
// pointer reports ErrRepositoryNotExists like an absent one.
//
// The helper asserts its own hostility before returning, so a fixture that
// quietly stopped being broken cannot make the tests below pass for the wrong
// reason.
func brokenRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("garbage, not a gitdir pointer\n"), 0o644))

	_, err := gitutil.FindRoot(dir)
	require.Error(t, err, "fixture must not resolve as a repository")
	require.False(t, gitutil.IsNoRepository(err),
		"fixture must fail as a BROKEN repository, not as an absent one")
	return dir
}

// TestGitutil_NoRepositoryIsDistinguishableFromABrokenOne pins the
// classification the resolution chain needs and cannot make for itself:
// gitutil.FindRoot wraps "this directory is simply not inside a repository"
// and "there IS a .git here and it is unreadable/corrupt" behind the same
// error prefix, so a caller reading only `err != nil` cannot tell the expected
// benign case from a real, reportable fault.
func TestGitutil_NoRepositoryIsDistinguishableFromABrokenOne(t *testing.T) {
	_, err := gitutil.FindRoot(t.TempDir())
	require.Error(t, err)
	assert.True(t, gitutil.IsNoRepository(err),
		"a directory outside any repository must classify as the benign case")

	// brokenRepoDir asserts the other side of the discrimination as part of
	// building the fixture: a .git that exists but is unusable must NOT
	// classify as "not a repository".
	brokenRepoDir(t)
}

// TestWorkDirWithBoundary_WarnsOnAnUnusableRepository pins what the
// classification buys. The chain treated EVERY git failure as "not inside a
// repo" and dropped silently to the bare cwd — the right fallback but the
// wrong silence: falling back to cwd keys this project's identity (its task
// log, its sessions) on the launch directory, and doing that because a
// repository the user does have is unreadable is a fault they have to be told
// about.
func TestWorkDirWithBoundary_WarnsOnAnUnusableRepository(t *testing.T) {
	dir := brokenRepoDir(t)
	testsupport.Isolate(t)
	testsupport.ChangeDir(t, dir)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	root, found, err := WorkDirWithBoundary()
	require.NoError(t, err)
	assert.False(t, found, "an unusable repository is not a project boundary")
	assert.NotEmpty(t, root)

	assert.Contains(t, sink.String(), "git", "the warning must name what failed")
	assert.Contains(t, sink.String(), "gitdir", "the warning must carry the underlying git failure")
}

// TestWorkDirWithBoundary_SilentOutsideAnyRepository is the other half: being
// outside a repository is expected and must stay silent, or the warning above
// stops carrying information.
func TestWorkDirWithBoundary_SilentOutsideAnyRepository(t *testing.T) {
	testsupport.ProjectDir(t) // isolate env + chdir to a fresh non-git temp dir

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	_, found, err := WorkDirWithBoundary()
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, sink.String(), "being outside a repository is not a fault and must not warn")
}
