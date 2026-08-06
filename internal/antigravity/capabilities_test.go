package antigravity

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// TestWriteCommandFiles_SkipsTraversalNames verifies skill names from bundle
// content (potentially remote) cannot write outside .agents/skills/: absolute
// and ".."-bearing names are skipped, while plain and nested names still land
// as `<name>/SKILL.md` directories (G3: the shape agy's skill scanner
// actually discovers, not the old flat `<name>.md` silent no-op).
func TestWriteCommandFiles_SkipsTraversalNames(t *testing.T) {
	fs := afero.NewMemMapFs()
	cmds := []agent.CommandExport{
		{Name: "../escape", Content: "evil", Enabled: true},
		{Name: "/abs/path", Content: "evil", Enabled: true},
		{Name: "a/../../b", Content: "evil", Enabled: true},
		{Name: "good", Content: "fine", Enabled: true},
		{Name: "group/cmd", Content: "nested fine", Enabled: true},
	}
	require.NoError(t, WriteCommandFiles("/work", cmds, agent.WithCommandFS(fs)))

	skillsDir := filepath.Join("/work", ".agents", "skills")
	for _, p := range []string{
		filepath.Join(skillsDir, "good", "SKILL.md"),
		filepath.Join(skillsDir, "group", "cmd", "SKILL.md"),
	} {
		exists, err := afero.Exists(fs, p)
		require.NoError(t, err)
		assert.True(t, exists, "legit skill %s must be written", p)
	}
	// Assert absence at the ACTUAL join sites the writer would use if the
	// SafeCommandRelPath guard regressed — not at paths the code can never
	// reach. filepath.Join(skillsDir, <rendered name>) resolves each traversal
	// name to: "../escape" -> /work/.agents/escape/SKILL.md; "/abs/path" ->
	// /work/.agents/skills/abs/path/SKILL.md (leading slash is a separator,
	// not an escape); "a/../../b" -> /work/.agents/b/SKILL.md (escapes up
	// into .agents/).
	for _, p := range []string{
		"/work/.agents/escape/SKILL.md",          // ../escape escapes to .agents/
		"/work/.agents/skills/abs/path/SKILL.md", // /abs/path joins under skills dir
		"/work/.agents/b/SKILL.md",               // a/../../b escapes to .agents/
	} {
		exists, err := afero.Exists(fs, p)
		require.NoError(t, err)
		assert.False(t, exists, "traversal name must not write %s", p)
	}

	manifest, err := afero.ReadFile(fs, filepath.Join(skillsDir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "good/SKILL.md")
	assert.Contains(t, string(manifest), "group/cmd/SKILL.md")
	assert.NotContains(t, string(manifest), "escape")
	assert.NotContains(t, string(manifest), "abs")
	// The a/../../b case had no compensating manifest signal; its rendered line
	// would be "b/SKILL.md", so a regression that re-enabled the traversal is
	// caught here.
	assert.NotContains(t, string(manifest), "b/SKILL.md")

	// Payload: the frontmatter is generated and the body carries the command
	// content verbatim, so agy's parser (name+description YAML keys) actually
	// has something to read.
	good, err := afero.ReadFile(fs, filepath.Join(skillsDir, "good", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(good), "name: \"good\"")
	assert.Contains(t, string(good), "description: \"good\"") // Description empty -> falls back to Name
	assert.Contains(t, string(good), "fine")
}

// TestWriteCommandFiles_ManifestTraversalLinesNotDeleted verifies the
// pre-write manifest cleanup never follows a doctored manifest line outside
// the skills tree, while legit stale entries are still removed.
func TestWriteCommandFiles_ManifestTraversalLinesNotDeleted(t *testing.T) {
	fs := afero.NewMemMapFs()
	skillsDir := filepath.Join("/work", ".agents", "skills")
	require.NoError(t, afero.WriteFile(fs, "/work/victim.txt", []byte("keep"), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(skillsDir, "old", "SKILL.md"), []byte("stale"), 0644))
	manifest := "../../victim.txt\tcommands\n/work/victim.txt\tcommands\nold/SKILL.md\tcommands\n"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(skillsDir, ledger.Name), []byte(manifest), 0644))

	cmds := []agent.CommandExport{{Name: "new", Content: "x", Enabled: true}}
	require.NoError(t, WriteCommandFiles("/work", cmds, agent.WithCommandFS(fs)))

	exists, err := afero.Exists(fs, "/work/victim.txt")
	require.NoError(t, err)
	assert.True(t, exists, "manifest traversal line must not delete outside the skills tree")
	exists, err = afero.Exists(fs, filepath.Join(skillsDir, "old", "SKILL.md"))
	require.NoError(t, err)
	assert.False(t, exists, "legit stale manifest entry still removed")
}

// TestWriteCommandFiles_DescriptionUsed verifies a non-empty Description rides
// into the generated frontmatter rather than always falling back to Name.
func TestWriteCommandFiles_DescriptionUsed(t *testing.T) {
	fs := afero.NewMemMapFs()
	cmds := []agent.CommandExport{{Name: "reviewer", Description: "Review a diff", Content: "do the review", Enabled: true}}
	require.NoError(t, WriteCommandFiles("/work", cmds, agent.WithCommandFS(fs)))

	body, err := afero.ReadFile(fs, filepath.Join("/work", ".agents", "skills", "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(body), "description: \"Review a diff\"")
	assert.Contains(t, string(body), "do the review")
}
