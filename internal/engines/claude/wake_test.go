package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Claude's wake is its cross-session messaging socket, and it is NOT bound
// until a bypass-permissions session is measured to accept the runner's post:
// declared absent, with the reason, rather than guessed.
func TestWake_ClaudeDeclaresItsWakeAbsentUntilTheSocketIsMeasured(t *testing.T) {
	w := Claude{}.Wake()
	assert.True(t, w.Decided())
	_, ok := w.Get()
	assert.False(t, ok)
	assert.Contains(t, w.AbsentReason(), "socket")
}
