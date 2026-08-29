//go:build parked_engines

package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

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
