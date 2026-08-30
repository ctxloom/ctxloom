package projectroot

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestParseGitdirPointer(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		wantPath string
		wantOk   bool
	}{
		{"plain", "gitdir: /a/b/.git/worktrees/foo\n", "/a/b/.git/worktrees/foo", true},
		{"no_trailing_newline", "gitdir: /a/b/.git/worktrees/foo", "/a/b/.git/worktrees/foo", true},
		{"crlf", "gitdir: /a/b/.git/worktrees/foo\r\n", "/a/b/.git/worktrees/foo", true},
		{"leading_blank_lines", "\n\ngitdir: /a/b/.git/worktrees/foo\n", "/a/b/.git/worktrees/foo", true},
		{"extra_whitespace", "gitdir:    /a/b/.git/worktrees/foo   \n", "/a/b/.git/worktrees/foo", true},
		{"not_a_pointer", "not a gitdir file\n", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, ok := parseGitdirPointer(tc.contents)
			assert.Equal(t, tc.wantOk, ok)
			assert.Equal(t, tc.wantPath, path)
		})
	}
}

func TestDetectWorktree(t *testing.T) {
	t.Run("no_git_at_all", func(t *testing.T) {
		dir := t.TempDir()
		info, err := DetectWorktree(afero.NewOsFs(), dir)
		require.NoError(t, err)
		assert.False(t, info.Linked, "a plain, non-git directory is never a worktree")
	})

	t.Run("git_is_a_directory", func(t *testing.T) {
		// The main worktree (or a plain, non-worktree repo): .git is a
		// directory, not a gitdir pointer file.
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
		info, err := DetectWorktree(afero.NewOsFs(), dir)
		require.NoError(t, err)
		assert.False(t, info.Linked)
	})

	t.Run("submodule_shaped_gitdir_file_is_not_a_worktree", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"),
			[]byte("gitdir: ../.git/modules/sub\n"), 0o644))
		info, err := DetectWorktree(afero.NewOsFs(), dir)
		require.NoError(t, err, "a submodule's gitdir file is well-formed, just not worktree-shaped")
		assert.False(t, info.Linked, "no /worktrees/ path segment -- must not be mistaken for a worktree")
	})

	t.Run("malformed_git_file_is_not_a_worktree", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir pointer"), 0o644))
		info, err := DetectWorktree(afero.NewOsFs(), dir)
		require.NoError(t, err)
		assert.False(t, info.Linked)
	})

	t.Run("unreadable_git_file_is_a_surfaced_error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root ignores file permissions")
		}
		dir := t.TempDir()
		p := filepath.Join(dir, ".git")
		require.NoError(t, os.WriteFile(p, []byte("gitdir: /x/.git/worktrees/y\n"), 0o644))
		require.NoError(t, os.Chmod(p, 0o000))
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) }) // let TempDir cleanup remove it
		_, err := DetectWorktree(afero.NewOsFs(), dir)
		assert.Error(t, err, "an unreadable .git file must surface, not silently resolve as 'not a worktree'")
	})

	t.Run("real_linked_worktree_resolves_the_main_root", func(t *testing.T) {
		main, linked := taskstest.RealGitWorktreeFixture(t)
		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked)
		assert.Equal(t, main, info.MainRoot)
		assert.True(t, info.MainRootExists)
	})

	t.Run("real_main_worktree_is_not_linked", func(t *testing.T) {
		main, _ := taskstest.RealGitWorktreeFixture(t)
		info, err := DetectWorktree(afero.NewOsFs(), main)
		require.NoError(t, err)
		assert.False(t, info.Linked, "the main worktree's .git is a directory, not a linked pointer")
	})

	t.Run("bare_clone_worktree_has_no_derivable_main_root", func(t *testing.T) {
		// A `git clone --bare url repo.git; git worktree add ../wt`
		// layout — the gitdir pointer is /p/repo.git/worktrees/wt, so the
		// naive filepath.Dir(filepath.Dir(commonDir)) derivation used to
		// compute mainRoot=/p, an EXISTING, unrelated directory (the parent
		// of the bare clone). MainRootExists must NOT be true here: there is
		// no main WORKTREE to derive at all when commonDir isn't named
		// ".git".
		root := t.TempDir()
		bareCommon := filepath.Join(root, "repo.git")
		require.NoError(t, os.MkdirAll(filepath.Join(bareCommon, "worktrees", "wt"), 0o755))
		linked := filepath.Join(root, "wt")
		require.NoError(t, os.MkdirAll(linked, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(linked, ".git"),
			[]byte("gitdir: "+filepath.Join(bareCommon, "worktrees", "wt")+"\n"), 0o644))

		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked, "it is still a linked worktree — just one with no derivable main root")
		assert.False(t, info.MainRootExists,
			"a bare-clone/separate-git-dir layout must not silently report the wrong directory (root's parent) as an existing main root")
	})

	t.Run("separate_git_dir_worktree_has_no_derivable_main_root", func(t *testing.T) {
		// `git init --separate-git-dir=/elsewhere/foo.git` — same shape as
		// the bare-clone case: commonDir is not named ".git".
		root := t.TempDir()
		common := filepath.Join(root, "elsewhere", "foo.git")
		require.NoError(t, os.MkdirAll(filepath.Join(common, "worktrees", "wt"), 0o755))
		linked := filepath.Join(root, "wt")
		require.NoError(t, os.MkdirAll(linked, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(linked, ".git"),
			[]byte("gitdir: "+filepath.Join(common, "worktrees", "wt")+"\n"), 0o644))

		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked)
		assert.False(t, info.MainRootExists)
	})

	t.Run("stale_gitdir_pointer_reports_main_root_missing", func(t *testing.T) {
		main, linked := taskstest.RealGitWorktreeFixture(t)
		require.NoError(t, os.RemoveAll(main))
		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked)
		assert.Equal(t, main, info.MainRoot)
		assert.False(t, info.MainRootExists, "the main worktree was deleted out from under the pointer")
	})
}

// TestDetectWorktree_EmptyDirIsRefused pins that dir was never validated,
// and filepath.Join("", ".git") is the BARE RELATIVE ".git" — so an empty dir
// silently stopped naming a directory at all and started probing whatever
// working directory the process happened to hold. The answer then depends on
// where the binary was launched from, which is precisely the property every
// caller of this package is trying to pin down. An unnamed directory is a
// caller mistake, not a directory to guess at.
func TestDetectWorktree_EmptyDirIsRefused(t *testing.T) {
	// Hostile fixture: the process cwd IS a linked-worktree-shaped directory,
	// so a dir="" that leaks to the cwd answers Linked=true and is visibly
	// distinguishable from a refusal (§11k).
	cwd := testsupport.ProjectDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, ".git"),
		[]byte("gitdir: "+filepath.Join(cwd, ".git", "worktrees", "wt")+"\n"), 0o644))
	probe, err := DetectWorktree(afero.NewOsFs(), cwd)
	require.NoError(t, err)
	require.True(t, probe.Linked, "fixture is not hostile: the cwd must look like a linked worktree for this test to mean anything")

	_, err = DetectWorktree(afero.NewOsFs(), "")
	assert.Error(t, err, "an empty dir must be refused, not resolved against the process working directory")
}

// TestTaskStoreRoot_EmptyDirIsRefused pins the same defect at the other entry
// point. TaskStoreRoot's first act is
// filepath.Join(dir, ".ctxloom"), which for an empty dir tests the cwd's
// .ctxloom and can return "" as a project root — a task store keyed on nothing.
func TestTaskStoreRoot_EmptyDirIsRefused(t *testing.T) {
	cwd := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".ctxloom"), 0o755))

	got, err := TaskStoreRoot(afero.NewOsFs(), "")
	assert.Error(t, err, "an empty dir must be refused, not resolved against the process working directory")
	assert.Empty(t, got, "a refused resolution must not hand back a root")
}

// TestDetectWorktree_UnstattableGitSurfacesAsError pins that the stat of
// dir/.git treated EVERY failure as "no .git here — not a worktree", while the
// ReadFile ten lines below surfaces the identical permission fault as an error.
// The asymmetry fails OPEN in the direction that loses data: a linked worktree
// whose .git cannot be stat'd is reported as an ordinary directory, so
// TaskStoreRoot keeps the worktree's own store and config's worktreeSignpost
// says nothing — the run looks healthy and the tasks die with the worktree.
// Only "the entry does not exist" means "not a worktree"; anything else is a
// fault, and faults are surfaced.
func TestDetectWorktree_UnstattableGitSurfacesAsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores file permissions")
	}
	dir := unstattableWorktreeDir(t)

	_, err := DetectWorktree(afero.NewOsFs(), dir)
	assert.Error(t, err, "a .git that cannot be stat'd must surface, not silently resolve as 'not a worktree'")
}

// TestTaskStoreRoot_UnreadableMarkerSurfacesAsError pins the same asymmetry at
// taskstore.go's opt-out probe, which reads dir's project-id marker: only plain
// ABSENCE may mean "no opt-out here". A permission fault treated as absence
// falls into the branch that redirects the task store somewhere else entirely,
// discarding an opt-out that may well exist.
func TestTaskStoreRoot_UnreadableMarkerSurfacesAsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores file permissions")
	}
	dir := unstattableWorktreeDir(t)

	got, err := TaskStoreRoot(afero.NewOsFs(), dir)
	assert.Error(t, err, "an unreadable project-id marker must surface, not be read as 'no marker here'")
	assert.Empty(t, got)
}

// unstattableWorktreeDir builds a directory that IS a linked worktree and whose
// entries cannot be stat'd, by removing search permission from the directory
// itself (stat of any child then fails EACCES, not ENOENT).
//
// It asserts the fault is visible from the code under test's own vantage point
// before handing the directory back: a fixture that merely looks broken, but
// whose stats actually succeed, would leave both pins green for a reason that
// has nothing to do with the defect (§11k).
func unstattableWorktreeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	require.NoError(t, os.MkdirAll(filepath.Join(common, "worktrees", "wt"), 0o755))

	dir := filepath.Join(root, "wt")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"),
		[]byte("gitdir: "+filepath.Join(common, "worktrees", "wt")+"\n"), 0o644))

	// Readable: prove it is a linked worktree, so "not a worktree" can only
	// ever be a misclassification.
	info, err := DetectWorktree(afero.NewOsFs(), dir)
	require.NoError(t, err)
	require.True(t, info.Linked, "fixture is not a linked worktree before the permission change")

	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// And prove the stats the code under test performs now fail, and fail with
	// something other than "does not exist".
	for _, name := range []string{".git", ".ctxloom"} {
		_, statErr := os.Stat(filepath.Join(dir, name))
		require.Error(t, statErr, "fixture is not hostile: stat of %s still succeeds", name)
		require.False(t, errors.Is(statErr, iofs.ErrNotExist),
			"fixture is not hostile: stat of %s reports plain absence, which really is 'not a worktree'", name)
	}
	return dir
}
