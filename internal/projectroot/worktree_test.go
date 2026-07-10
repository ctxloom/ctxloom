package projectroot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		main, linked := realGitWorktreeFixture(t)
		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked)
		assert.Equal(t, main, info.MainRoot)
		assert.True(t, info.MainRootExists)
	})

	t.Run("real_main_worktree_is_not_linked", func(t *testing.T) {
		main, _ := realGitWorktreeFixture(t)
		info, err := DetectWorktree(afero.NewOsFs(), main)
		require.NoError(t, err)
		assert.False(t, info.Linked, "the main worktree's .git is a directory, not a linked pointer")
	})

	t.Run("stale_gitdir_pointer_reports_main_root_missing", func(t *testing.T) {
		main, linked := realGitWorktreeFixture(t)
		require.NoError(t, os.RemoveAll(main))
		info, err := DetectWorktree(afero.NewOsFs(), linked)
		require.NoError(t, err)
		assert.True(t, info.Linked)
		assert.Equal(t, main, info.MainRoot)
		assert.False(t, info.MainRootExists, "the main worktree was deleted out from under the pointer")
	})
}

// realGitWorktreeFixture creates a real git repo (main, with one commit) and a
// LINKED worktree of it (linked) via `git worktree add`, each under its own
// fresh t.TempDir(). Skips the test if git isn't on PATH. Returns both roots
// as absolute, symlink-resolved paths so they compare equal to whatever git
// itself resolved into the gitdir pointer.
func realGitWorktreeFixture(t *testing.T) (main, linked string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	mainDir := t.TempDir()
	runGit(t, mainDir, "init", "-q")
	runGit(t, mainDir, "config", "user.email", "test@example.com")
	runGit(t, mainDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(mainDir, "f.txt"), []byte("x"), 0o644))
	runGit(t, mainDir, "add", "f.txt")
	runGit(t, mainDir, "commit", "-q", "-m", "init")

	linkedDir := filepath.Join(t.TempDir(), "linked-wt")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "wt-branch", linkedDir)

	main, err := filepath.EvalSymlinks(mainDir)
	require.NoError(t, err)
	linked, err = filepath.EvalSymlinks(linkedDir)
	require.NoError(t, err)
	return main, linked
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
