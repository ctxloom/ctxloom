package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
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

// TestInteractiveTerminal_RestoreSwitchesOffEngineModes pins the terminal-mode
// reset on the restore every interactive exit path runs (prepareSessionIO
// composes it under the terminal layer's Close; the drive defers it and calls
// it inline). An engine enables focus reporting (DECSET 1004), bracketed paste
// and mouse tracking for itself; when it does not switch them off — killed,
// crashed, or its teardown bytes never drained — restoring termios alone
// leaves the terminal reporting into the user's shell, which echoes every
// focus change as ^[[I^[[O. The reset must come out exactly once however many
// paths call restore, and before raw mode is released.
func TestInteractiveTerminal_RestoreSwitchesOffEngineModes(t *testing.T) {
	origIsTerminal, origMakeRaw, origRestore, origSink := termIsTerminal, termMakeRaw, termRestore, termModeResetSink
	t.Cleanup(func() {
		termIsTerminal, termMakeRaw, termRestore, termModeResetSink = origIsTerminal, origMakeRaw, origRestore, origSink
	})
	_ = captureWarnings(t) // the resize watcher may warn about the test's non-tty stdin

	var out bytes.Buffer
	var events []string
	termIsTerminal = func(int) bool { return true }
	termMakeRaw = func(int) (*term.State, error) { return &term.State{}, nil }
	termRestore = func(int, *term.State) error {
		events = append(events, "termios restored")
		return nil
	}
	termModeResetSink = func() io.Writer {
		events = append(events, "modes reset")
		return &out
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, _, restore := interactiveTerminal(ctx)
	restore()
	restore() // the deferred call after the inline one

	for _, mode := range []string{
		"\x1b[?1004l", "\x1b[?2004l", // focus reporting, bracketed paste
		"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", // mouse tracking
		"\x1b[?1005l", "\x1b[?1006l", "\x1b[?1015l", // every mouse coordinate encoding
	} {
		assert.Equal(t, 1, strings.Count(out.String(), mode), "restore must switch off %q exactly once", mode)
	}
	assert.Contains(t, out.String(), "\x1b[?25h", "restore must leave the cursor visible")
	assert.Equal(t, []string{"modes reset", "termios restored"}, events,
		"the mode reset is written once, while the terminal is still the session's, then termios is restored")
}
