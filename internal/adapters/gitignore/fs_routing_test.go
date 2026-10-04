package gitignore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The fs-taking entry points read and write through the fs they are handed.
// Every file lives ONLY in a MemMapFs, under a project path absent from disk,
// so an answer reached through the OS filesystem would find nothing and a
// write through it would fail or land outside the fs.
func TestGitignore_ReadsAndWritesThroughTheGivenFs(t *testing.T) {
	project := filepath.Join(t.TempDir(), "absent-from-disk", "project")
	root := filepath.Join(project, ".gitignore")

	seed := func(t *testing.T, content string) afero.Fs {
		t.Helper()
		fsys := afero.NewMemMapFs()
		require.NoError(t, fsys.MkdirAll(project, 0o755))
		testsupport.WriteFile(t, fsys, root, []byte(content), 0o644)
		return fsys
	}

	t.Run("SupersededBlanketLines", func(t *testing.T) {
		lines, err := SupersededBlanketLines(seed(t, ".ctxloom/\n"), root)
		require.NoError(t, err)
		require.Equal(t, []string{".ctxloom/"}, lines)
	})

	t.Run("RedundantRootPatterns", func(t *testing.T) {
		found, err := RedundantRootPatterns(seed(t, PrivateStatePatterns[0]+"\n"), project)
		require.NoError(t, err)
		require.Equal(t, []string{PrivateStatePatterns[0]}, found)
	})

	t.Run("RetireSupersededFile", func(t *testing.T) {
		fsys := seed(t, "keep\n.ctxloom/\n")
		changed, err := RetireSupersededFile(fsys, root)
		require.NoError(t, err)
		require.True(t, changed)
		got, err := afero.ReadFile(fsys, root)
		require.NoError(t, err)
		require.Equal(t, "keep\n", string(got))
	})

	t.Run("RetireWorktreeConfigBlock", func(t *testing.T) {
		fsys := seed(t, "keep\n"+WorktreeComment+"\n"+WorktreeArtifactPatterns[0]+"\n")
		changed, err := RetireWorktreeConfigBlock(fsys, root)
		require.NoError(t, err)
		require.True(t, changed)
	})

	t.Run("EnsureNested", func(t *testing.T) {
		fsys := seed(t, ".ctxloom/\n")
		out, err := EnsureNested(fsys, project)
		require.NoError(t, err)
		require.True(t, out.Changed)
		require.Equal(t, []string{".ctxloom/"}, out.Retired)
		got, err := afero.ReadFile(fsys, NestedGitignorePath(project))
		require.NoError(t, err)
		require.Equal(t, string(nestedContent()), string(got))
		_, err = os.Stat(project)
		require.ErrorIs(t, err, os.ErrNotExist, "nothing may reach the OS filesystem")
	})
}
