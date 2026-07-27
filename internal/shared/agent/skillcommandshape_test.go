package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U102-F01: a command with empty Content used to render a valid-looking
// SKILL.md containing only frontmatter (name/description) — success, zero
// instruction bytes delivered to the engine. WriteManagedPackageFiles already
// warns-and-skips loudly on a render error ("skipping package %q: render
// failed: %v"), so returning an error here is enough to make the loss visible
// instead of materializing a hollow skill file.
func TestRenderCommandAsSkillFile_EmptyContentIsAnError(t *testing.T) {
	_, _, err := RenderCommandAsSkillFile(CommandExport{Name: "hollow", Description: "looks fine", Content: ""})
	require.Error(t, err, "empty Content must not render a success SKILL.md with zero instruction bytes")

	_, _, err = RenderCommandAsSkillFile(CommandExport{Name: "whitespace-only", Content: "   \n\t "})
	require.Error(t, err, "whitespace-only Content is the same zero-instruction-bytes failure")
}

func TestRenderCommandAsSkillFile_RealContentStillRenders(t *testing.T) {
	relPath, content, err := RenderCommandAsSkillFile(CommandExport{Name: "doctor", Description: "check things", Content: "Run the checks."})
	require.NoError(t, err)
	assert.Equal(t, "doctor/SKILL.md", relPath)
	assert.Contains(t, string(content), "Run the checks.")
	assert.Contains(t, string(content), "name: \"doctor\"")
}
