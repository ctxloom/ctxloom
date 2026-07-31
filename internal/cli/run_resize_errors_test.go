package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// TestWatchResize_UnsizableTerminalSaysSo pins the diagnostic on the initial
// size probe. watchResize's whole job is to tell the controller how big the
// user's terminal is; when term.GetSize fails there is no size to send, the
// pty keeps whatever default it was created with, and the agent renders into
// the wrong geometry for the rest of the session. Failing silently makes that
// look like a rendering bug in the engine rather than a probe that never
// answered.
func TestWatchResize_UnsizableTerminalSaysSo(t *testing.T) {
	warnings := captureWarnings(t)

	// A pipe is a valid *os.File that is not a terminal, so GetSize fails on
	// it exactly as it would on a terminal that refuses the ioctl.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close(); _ = w.Close() }()
	require.False(t, term.IsTerminal(int(r.Fd())))

	ch := watchResize(t.Context(), r)
	require.NotNil(t, ch)

	assert.Contains(t, warnings.String(), "terminal size",
		"a size probe that fails must say so — the pty silently keeps a default geometry otherwise")
}
