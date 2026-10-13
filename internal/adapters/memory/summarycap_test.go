package memory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestSessionCompactPrompt_TitleLimitMatchesTheCode (owner ruling #4b,
// 2026-10-12): the title limit the prompt asks the model for is the one
// sessions.FirstLineSummary enforces on the frontmatter summary. A prompt
// that asks for less wastes room the code keeps; one that asks for more
// gets its titles cut off.
func TestSessionCompactPrompt_TitleLimitMatchesTheCode(t *testing.T) {
	kept := len(sessions.FirstLineSummary(strings.Repeat("x", 200)))
	assert.Contains(t, sessionCompactPrompt, fmt.Sprintf("≤%d characters", kept),
		"the prompt's summary title limit must be the %d the code keeps", kept)
}
