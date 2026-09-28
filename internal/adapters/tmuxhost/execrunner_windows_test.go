package tmuxhost

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewExecRunner_RefusesOnWindows: the real probe, unstubbed, refuses on
// Windows with the typed error callers match on — even where an MSYS tmux is
// on PATH, since no launcher could run under it.
func TestNewExecRunner_RefusesOnWindows(t *testing.T) {
	_, err := NewExecRunner()
	assert.ErrorIs(t, err, ErrTmuxUnavailable)
	assert.ErrorIs(t, err, errNoPOSIXHosting)
}
