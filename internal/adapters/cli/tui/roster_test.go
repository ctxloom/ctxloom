package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

func TestStateGlyphs(t *testing.T) {
	assert.Equal(t, "●", stateGlyph("executing"))
	assert.Equal(t, "●", stateGlyph("live"))
	assert.Equal(t, "✓", stateGlyph("ended"))
	assert.Equal(t, "?", stateGlyph("unknown"))
	assert.Equal(t, "◐", stateGlyph("queued"))
	assert.Equal(t, "◐", stateGlyph("parked"))
	assert.Equal(t, "◐", stateGlyph("idle"))
}

// TestRosterStates_AreTheListingsWords: the roster and `ctxloom session list`
// say a session's state in one vocabulary, the port's — and the tree's
// ended word is the coordinator's, so that must be the port's too.
func TestRosterStates_AreTheListingsWords(t *testing.T) {
	assert.Equal(t, frontend.SessionLive, StateLive)
	assert.Equal(t, frontend.SessionEnded, StateEnded)
	assert.Equal(t, frontend.SessionUnknown, StateUnknown)
}
