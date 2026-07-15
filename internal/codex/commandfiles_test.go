package codex

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestWriteCommandFiles_GlobalPromptsDir verifies codex prompts are written flat
// to the GLOBAL $CODEX_HOME/prompts (codex discovers prompts only there, not in a
// project dir), with {{var}} transformed to positional $N and a manifest tracked.
func TestWriteCommandFiles_GlobalPromptsDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fake/codexhome")
	fs := afero.NewMemMapFs()

	cmds := []agent.CommandExport{{Name: "review", Content: "review {{target}}", Enabled: true}}
	require.NoError(t, WriteCommandFiles("/some/project", cmds, agent.WithCommandFS(fs)))

	// Written to the global prompts dir, NOT the project.
	exists, _ := afero.Exists(fs, "/fake/codexhome/prompts/review.md")
	assert.True(t, exists, "prompt written to $CODEX_HOME/prompts")
	projExists, _ := afero.Exists(fs, "/some/project/.codex/prompts/review.md")
	assert.False(t, projExists, "prompt is NOT written to the (unread) project dir")

	data, err := afero.ReadFile(fs, "/fake/codexhome/prompts/review.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), "$1", "{{target}} transformed to positional $1")

	// Manifest tracks the written file for scoped cleanup.
	man, err := afero.ReadFile(fs, "/fake/codexhome/prompts/.ctxloom-manifest")
	require.NoError(t, err)
	assert.Contains(t, string(man), "review.md")
}

// TestWriteCommandFiles_NoCodexHome_ProjectScoped is comfy-lion's PAYLOAD
// test (the commands-surface sibling of skillfiles_test.go's identical
// case): with NO $CODEX_HOME set and a real workDir, WriteCommandFiles must
// land under the workDir-scoped cellScopedPromptsDir, never the bare
// TRUE-global codexPromptsDir() (~/.codex/prompts) — this is exactly the
// residue this task found live on this box (~/.codex/prompts carrying
// ctxloom-managed check-triggers.md/discover.md/recover.md from a prior
// bare-cwd-fallback-to-$HOME run).
func TestWriteCommandFiles_NoCodexHome_ProjectScoped(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	fs := afero.NewMemMapFs()

	cmds := []agent.CommandExport{{Name: "review", Content: "review", Enabled: true}}
	require.NoError(t, WriteCommandFiles("/some/project", cmds, agent.WithCommandFS(fs)))

	exists, _ := afero.Exists(fs, "/some/project/.codex/prompts/review.md")
	assert.True(t, exists, "project-scoped, not the bare global ~/.codex")
}

// TestTransformToCodexPrompt_Frontmatter verifies description + argument-hint are
// emitted as YAML frontmatter (the keys codex supports) and {{var}} becomes $N.
func TestTransformToCodexPrompt_Frontmatter(t *testing.T) {
	out := TransformToCodexPrompt(agent.CommandExport{
		Name:         "pr",
		Description:  "Open a draft PR",
		ArgumentHint: "[TITLE=<title>]",
		Content:      "open pr for {{title}}",
		Enabled:      true,
	})
	assert.Contains(t, out, "---\n", "has frontmatter block")
	assert.Contains(t, out, "description: Open a draft PR")
	// argument-hint carries YAML-special chars ([], <>) so it must be quoted to
	// stay a scalar rather than parse as a flow sequence (codex-code-01-003).
	assert.Contains(t, out, `argument-hint: "[TITLE=<title>]"`)
	assert.Contains(t, out, "$1", "{{title}} -> positional $1")
}

// TestTransformToCodexPrompt_NoFrontmatter verifies a bare command (no metadata)
// produces just the body, no empty frontmatter block.
func TestTransformToCodexPrompt_NoFrontmatter(t *testing.T) {
	out := TransformToCodexPrompt(agent.CommandExport{Name: "x", Content: "just do it", Enabled: true})
	assert.Equal(t, "just do it", out)
}

// TestWriteCommandFiles_DisabledClearsManifest verifies that with no enabled
// exports the manifest is removed (nothing to track).
func TestWriteCommandFiles_DisabledClearsManifest(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fake/codexhome")
	fs := afero.NewMemMapFs()

	require.NoError(t, WriteCommandFiles("/p", nil, agent.WithCommandFS(fs)))
	exists, _ := afero.Exists(fs, "/fake/codexhome/prompts/.ctxloom-manifest")
	assert.False(t, exists)
}
