//go:build integration && !windows

package integration

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// This file is the F2 binary-level pty acceptance harness (Wave F playbook):
// it drives the actual COMPILED ctxloom binary's `run` command over a real
// pty (testenv.RunPTY — aymanbagabas/go-pty, no tmux/expect/vt10x, matching
// the playbook's binding tooling constraint), proving the split view
// (internal/adapters/cli/tui's tui.SplitView: the agent tree beside the
// engine's emulated terminal) engages through the REAL CLI dispatch and
// process lifecycle — the one seam the in-process harnesses
// (internal/adapters/cli/tui/split_program_test.go) cannot reach, since those
// start the split view directly rather than through `ctxloom run`.
//
// Every wait reads the display (testenv.PTYSession.AwaitDisplay), never the
// bytes: the split view's renderer draws by cursor address and redraws only
// the cells that changed, and the engine's output reaches the human through
// the pane's emulator, re-drawn, never verbatim.
//
// Gotcha for anyone extending this file: SetupMockLM writes config.yaml at
// ctxloomconfig.CurrentConfigVersion, so loading it applies no in-memory
// schema upgrade. A config behind it would make every run report the pending
// rewrite on stderr (internal/adapters/cli/run.go's confirmUpgrade) unless
// -y (runAssumeYes) applies it.
//
// Ordering: a mock turn ends the instant it has written its reply, and the
// split view closes with the session. A keystroke whose effect is asserted is
// therefore sent only to a session the test has PROVED is live and reading:
// the mock runs its interactive echo (CTXLOOM_MOCK_ECHO_STDIN=1), the test
// types a sentinel line and waits for the engine to echo it, and the session
// ends only when the test types "quit".

//
// Every wait is for an event (bytes the pty delivers, the process exiting)
// and is bounded only by the test binary's deadline (testsupport.Expiry): the
// plugin subprocess spawn and the engine behind it run at whatever pace a
// loaded machine allows, so no fixed cap separates late from broken.

const ptyRows, ptyCols = 24, 80

// setupPTYTestEnv builds an env wired for a MockLM-backed `ctxloom run` over
// a pty: the fragment gives run an explicit context source, so it never
// falls through to default-agent resolution (irrelevant to this file, and a
// fatal finding on an unconfigured default agent — see run.go's startup
// gate).
func setupPTYTestEnv(t *testing.T) *testenv.TestEnvironment {
	t.Helper()
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "viewer-fragment", nil, "viewer pty test content")
	return env
}

// startLiveViewerSession starts `ctxloom run` over a pty with the mock
// engine held open on its interactive echo, and returns once the engine has
// echoed a typed sentinel: proof that the session is live and that keystrokes
// reach the engine through whatever the run put between pty and engine.
func startLiveViewerSession(t *testing.T, args ...string) *testenv.PTYSession {
	t.Helper()
	env := setupPTYTestEnv(t)
	sess, err := env.RunPTY(ptyCols, ptyRows, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, append([]string{"run"}, args...)...)
	require.NoError(t, err)
	t.Cleanup(sess.Close)
	typeLineAndAwaitEcho(t, sess, "session-live")
	return sess
}

// typeLineAndAwaitEcho types line into the session and waits for the
// display to show the mock engine's echo of it.
func typeLineAndAwaitEcho(t *testing.T, sess *testenv.PTYSession, line string) {
	t.Helper()
	_, err := sess.Write([]byte(line + "\n"))
	require.NoError(t, err)
	display, echoed := sess.AwaitDisplay(t, func(display string) bool { return strings.Contains(display, "mock echo: "+line) })
	require.True(t, echoed, "the engine never echoed %q; the display:\n%s", line, display)
}

// quitAndAwaitCleanExit ends the held mock session and requires a clean exit.
func quitAndAwaitCleanExit(t *testing.T, sess *testenv.PTYSession) {
	t.Helper()
	_, err := sess.Write([]byte("quit\n"))
	require.NoError(t, err)
	exited, waitErr := sess.AwaitExit(t)
	require.True(t, exited, "ctxloom run never exited; captured: %q", sess.Output())
	require.NoError(t, waitErr)
	assert.Equal(t, 0, sess.ExitCode())
}

// splitDivider is the split view's divider, at column rosterPaneWidth on
// every row; splitPaneHint is the panel's last line while the pane holds the
// keyboard, splitTreeHint its first words once the tree does.
const (
	splitDivider  = "│"
	splitPaneHint = "^] agents"
	splitTreeHint = "j/k move"
)

// leftAltScreen reports whether out ends off the alternate screen: the split
// view took the human's terminal and gave it back.
func leftAltScreen(out string) bool {
	return strings.Contains(out, "\x1b[?1049h") && strings.LastIndex(out, "\x1b[?1049l") > strings.LastIndex(out, "\x1b[?1049h")
}

// TestRunPTY_SplitViewDrawsTheTreeBesideTheEngine: `ctxloom run` on a tty
// draws the split view — the panel, its divider on every row, and the
// engine's own output in the pane to the divider's right, typed input
// reaching it there — and gives the terminal back on exit.
func TestRunPTY_SplitViewDrawsTheTreeBesideTheEngine(t *testing.T) {
	sess := startLiveViewerSession(t, "-f", "viewer-fragment", "hello from the split test")

	// The engine is sized to the pane once the human's terminal reports its
	// colours or the wait for them passes: from then on its rows fit beside
	// the panel, none cut short.
	display, fitted := sess.AwaitDisplay(t, func(display string) bool {
		for _, row := range strings.Split(display, "\n") {
			if _, pane, ok := strings.Cut(row, splitDivider); !ok || strings.Contains(pane, "…") {
				return false
			}
		}
		return true
	})
	require.True(t, fitted, "the engine's rows never fit the pane; the display:\n%s", display)
	rows := strings.Split(display, "\n")
	require.Len(t, rows, ptyRows, "the display is the pty's size")
	echo := rows[slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, "mock echo: session-live") })]
	assert.Less(t, strings.Index(echo, splitDivider), strings.Index(echo, "mock echo"), "the engine draws in the pane, right of the divider")
	assert.Contains(t, display, splitPaneHint, "the panel names the prefix that reaches the tree")

	quitAndAwaitCleanExit(t, sess)
	assert.True(t, leftAltScreen(sess.Output()), "process exit leaves the split view's alternate screen")

	// The main screen keeps the engine's final frame, without the panel,
	// and the line naming the session's transcript under it.
	main := sess.Display()
	assert.Contains(t, main, "mock echo: session-live", "the engine's final frame is left on the main screen")
	assert.NotContains(t, main, splitPaneHint, "the panel is not")
	label, _, _ := strings.Cut(cli.SplitTranscriptLine, "%s")
	assert.Contains(t, main, label, "the transcript line follows the frame")
	out := sess.Output()
	tail := out[strings.LastIndex(out, "\x1b[?1049l"):]
	assert.Contains(t, tail, filepath.Join(paths.TranscriptsDirName, paths.CanonicalTranscriptFileName)+"\r\n",
		"it names the session's canonical transcript")
}

// TestRunPTY_CtrlBracketMovesFocusToTheTree drives the Ctrl-] prefix
// through the real binary against a live session: the tree takes the
// keyboard (its key hints replace the pane's), 'q' hands it back, and
// keystrokes reach the engine again.
func TestRunPTY_CtrlBracketMovesFocusToTheTree(t *testing.T) {
	sess := startLiveViewerSession(t, "-f", "viewer-fragment", "hello from the focus test")

	_, err := sess.Write([]byte{0x1d})
	require.NoError(t, err)
	display, tree := sess.AwaitDisplay(t, func(display string) bool { return strings.Contains(display, splitTreeHint) })
	require.True(t, tree, "the prefix never moved focus to the tree; the display:\n%s", display)

	_, err = sess.Write([]byte{'q'})
	require.NoError(t, err)
	display, pane := sess.AwaitDisplay(t, func(display string) bool {
		return !strings.Contains(display, splitTreeHint) && strings.Contains(display, splitPaneHint)
	})
	require.True(t, pane, "'q' never handed the keyboard back to the pane; the display:\n%s", display)
	typeLineAndAwaitEcho(t, sess, "after-release")

	quitAndAwaitCleanExit(t, sess)
	assert.True(t, leftAltScreen(sess.Output()), "process exit leaves the split view's alternate screen")
}

// TestRunPTY_PlainTerminalNeverEngages proves --plain-terminal opts a pty
// session out of the split view: the engine's bytes reach the terminal as
// they are, a Ctrl-] typed into a live session reaches the engine as a
// literal byte, and the split view never takes the screen (run.go starts it
// only when !runPlainTerminal).
func TestRunPTY_PlainTerminalNeverEngages(t *testing.T) {
	env := setupPTYTestEnv(t)
	sess, err := env.RunPTY(ptyCols, ptyRows, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "--plain-terminal", "-f", "viewer-fragment", "hello from the plain-terminal test")
	require.NoError(t, err)
	t.Cleanup(sess.Close)

	for _, line := range []string{"session-live", "\x1dliteral-prefix"} {
		_, err := sess.Write([]byte(line + "\n"))
		require.NoError(t, err)
		out, echoed := sess.AwaitOutput(t, func(out string) bool { return strings.Contains(out, "mock echo: "+line) })
		require.True(t, echoed, "the engine's echo of %q never reached the terminal verbatim; captured: %q", line, out)
	}

	quitAndAwaitCleanExit(t, sess)
	out := sess.Output()
	assert.NotContains(t, out, "\x1b[?1049h", "--plain-terminal never takes the alternate screen")
	assert.NotContains(t, sess.Display(), splitPaneHint, "--plain-terminal never draws the panel")
	label, _, _ := strings.Cut(cli.SplitTranscriptLine, "%s")
	assert.NotContains(t, out, label, "nor leaves the split view's transcript line")
}
