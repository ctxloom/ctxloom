package agent

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderNameContent maps a command to <name>.md carrying its Content verbatim —
// the minimal renderer the dedup tests drive WriteManagedCommandFiles through.
// The per-agent writers layer frontmatter/argument transforms on top, but the
// cross-scope dedup is agnostic to that and keys purely on the rendered bytes.
func renderNameContent(c CommandExport) (string, []byte, error) {
	return c.Name + ".md", []byte(c.Content), nil
}

// TestWriteManagedCommandFiles_DedupHomeDir covers the cross-scope
// ("home/global wins") dedup: a project copy byte-identical to the same-named
// file in the home commands dir is neither written nor manifest-tracked, while
// any divergence, an absent home file, or the target being the home dir itself
// all write normally. A skip also self-heals once the home copy disappears.
func TestWriteManagedCommandFiles_DedupHomeDir(t *testing.T) {

	t.Run("identical_home_copy_skips_and_omits_from_manifest", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		home := "/home/.claude/commands"
		proj := "/proj/.claude/commands"
		require.NoError(t, afero.WriteFile(fs, filepath.Join(home, "recover.md"), []byte("BODY"), 0644))

		cmds := []CommandExport{
			{Name: "recover", Content: "BODY", Enabled: true}, // identical to home → skipped
			{Name: "other", Content: "OTHER", Enabled: true},  // project-unique → written
		}
		require.NoError(t, WriteManagedCommandFiles(fs, proj, cmds, renderNameContent, WithDedupHomeDir(home)))

		exists, _ := afero.Exists(fs, filepath.Join(proj, "recover.md"))
		assert.False(t, exists, "an identical home copy must not be duplicated into the project scope")
		exists, _ = afero.Exists(fs, filepath.Join(proj, "other.md"))
		assert.True(t, exists, "a project-unique command is still written")

		man, err := afero.ReadFile(fs, filepath.Join(proj, ledger.Name))
		require.NoError(t, err)
		assert.NotContains(t, string(man), "recover.md", "a skipped command must not be manifest-tracked")
		assert.Contains(t, string(man), "other.md")
	})

	t.Run("divergent_home_copy_writes_normally", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		home := "/home/.claude/commands"
		proj := "/proj/.claude/commands"
		require.NoError(t, afero.WriteFile(fs, filepath.Join(home, "recover.md"), []byte("OLD"), 0644))

		cmds := []CommandExport{{Name: "recover", Content: "NEW", Enabled: true}}
		require.NoError(t, WriteManagedCommandFiles(fs, proj, cmds, renderNameContent, WithDedupHomeDir(home)))

		got, err := afero.ReadFile(fs, filepath.Join(proj, "recover.md"))
		require.NoError(t, err)
		assert.Equal(t, "NEW", string(got), "version skew must still be written, never silently hidden")
		man, _ := afero.ReadFile(fs, filepath.Join(proj, ledger.Name))
		assert.Contains(t, string(man), "recover.md")
	})

	t.Run("target_equal_to_home_never_self_dedups", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		dir := "/home/.claude/commands"
		require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "recover.md"), []byte("BODY"), 0644))

		// dedupHomeDir == dir: the rendered file is byte-identical to the copy
		// already on disk, but a dir must never dedup against itself.
		cmds := []CommandExport{{Name: "recover", Content: "BODY", Enabled: true}}
		require.NoError(t, WriteManagedCommandFiles(fs, dir, cmds, renderNameContent, WithDedupHomeDir(dir)))

		man, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
		require.NoError(t, err)
		assert.Contains(t, string(man), "recover.md", "writing into the home dir itself must not dedup")
	})

	t.Run("absent_home_file_writes_normally", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		home := "/home/.claude/commands" // never populated
		proj := "/proj/.claude/commands"

		cmds := []CommandExport{{Name: "recover", Content: "BODY", Enabled: true}}
		require.NoError(t, WriteManagedCommandFiles(fs, proj, cmds, renderNameContent, WithDedupHomeDir(home)))

		exists, _ := afero.Exists(fs, filepath.Join(proj, "recover.md"))
		assert.True(t, exists, "with no home copy to shadow it, the project command is written")
		man, _ := afero.ReadFile(fs, filepath.Join(proj, ledger.Name))
		assert.Contains(t, string(man), "recover.md")
	})

	t.Run("self_heals_after_home_file_removed", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		home := "/home/.claude/commands"
		proj := "/proj/.claude/commands"
		homeFile := filepath.Join(home, "recover.md")
		require.NoError(t, afero.WriteFile(fs, homeFile, []byte("BODY"), 0644))

		cmds := []CommandExport{{Name: "recover", Content: "BODY", Enabled: true}}
		// First pass: the identical home copy shadows it → skipped.
		require.NoError(t, WriteManagedCommandFiles(fs, proj, cmds, renderNameContent, WithDedupHomeDir(home)))
		exists, _ := afero.Exists(fs, filepath.Join(proj, "recover.md"))
		require.False(t, exists, "precondition: the identical home copy is skipped")

		// Remove the home copy and re-run: the project copy must reappear.
		require.NoError(t, fs.Remove(homeFile))
		require.NoError(t, WriteManagedCommandFiles(fs, proj, cmds, renderNameContent, WithDedupHomeDir(home)))
		exists, _ = afero.Exists(fs, filepath.Join(proj, "recover.md"))
		assert.True(t, exists, "once the home copy is gone, the project copy self-heals back in")
	})
}
