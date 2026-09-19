package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFrameProjectContext verifies the single-shot framing claude loads via
// --append-system-prompt-file: the attribution header + preamble, the content
// wrapped in a <ctxloom-context> block, and empty content yielding nothing.
func TestFrameProjectContext(t *testing.T) {
	t.Run("empty content yields empty string", func(t *testing.T) {
		assert.Equal(t, "", FrameProjectContext(""),
			"empty context must deliver nothing, not a bare 'content loaded' header")
	})

	t.Run("frames content with header, preamble, and envelope", func(t *testing.T) {
		out := FrameProjectContext("rust rules go here")

		assert.True(t, strings.HasPrefix(out, ProjectContextHeader),
			"framed context leads with the attribution header")
		assert.Contains(t, out, "Treat it as authoritative project instructions.",
			"the preamble tells the model how to treat the content")
		assert.Contains(t, out, "<ctxloom-context>")
		assert.Contains(t, out, "</ctxloom-context>")

		open := strings.Index(out, "<ctxloom-context>")
		body := strings.Index(out, "rust rules go here")
		close := strings.Index(out, "</ctxloom-context>")
		assert.True(t, open < body && body < close,
			"content sits inside the <ctxloom-context> block")
	})
}
