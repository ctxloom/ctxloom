package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// TestInteractiveTerminal_RawModeFailureSaysSo pins the diagnostic on the
// raw-mode transition. interactiveTerminal returns (nil, nil, no-op) for two
// completely different situations: stdin is not a terminal (normal, and the
// run proceeds without a pty owner), and stdin IS a terminal but could not be
// put in raw mode (abnormal — the user gets an interactive agent session that
// receives none of their keystrokes). Collapsing the second into the first
// leaves the user facing a dead prompt with nothing on stderr to explain it.
//
// term.MakeRaw cannot be driven to fail against a real terminal from a test,
// so it is reached through a seam here — the same technique run.go documents
// for execCommand.
func TestInteractiveTerminal_RawModeFailureSaysSo(t *testing.T) {
	warnings := captureWarnings(t)

	origIsTerminal, origMakeRaw := termIsTerminal, termMakeRaw
	t.Cleanup(func() { termIsTerminal, termMakeRaw = origIsTerminal, origMakeRaw })

	termIsTerminal = func(int) bool { return true }
	termMakeRaw = func(int) (*term.State, error) { return nil, errors.New("ioctl refused") }

	in, resize, restore := interactiveTerminal(t.Context())
	require.NotNil(t, restore, "restore must always be callable")
	restore()

	assert.Nil(t, in, "no keystroke source when raw mode was refused")
	assert.Nil(t, resize)
	assert.Contains(t, warnings.String(), "raw mode",
		"a refused raw-mode transition must be reported — it is indistinguishable from a non-terminal stdin otherwise")
	assert.Contains(t, warnings.String(), "ioctl refused", "the underlying cause must survive into the warning")
}
