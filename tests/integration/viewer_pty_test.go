//go:build integration && !windows

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// This file is the F2 binary-level pty acceptance harness (Wave F playbook):
// it drives the actual COMPILED ctxloom binary's `run` command over a real
// pty (testenv.RunPTY — aymanbagabas/go-pty, no tmux/expect/vt10x, matching
// the playbook's binding tooling constraint), proving the terminal
// observation layer (internal/adapters/termui's surround bar + prefix-key viewer)
// engages through the REAL CLI dispatch and process lifecycle — the one seam
// F1's in-process harnesses (internal/adapters/termui/overlay_composition_test.go,
// internal/adapters/cli/tui/overlay_test.go) cannot reach, since those construct
// termui.Controller/tui.Overlay directly rather than through `ctxloom run`.
//
// Gotcha for anyone extending this file: SetupMockLM writes config.yaml at
// ctxloomconfig.CurrentConfigVersion, so loading it applies no in-memory
// schema upgrade. A config behind it would make every run report the pending
// rewrite on stderr (internal/adapters/cli/run.go's confirmUpgrade) unless
// -y (runAssumeYes) applies it.
//
// Ordering: a mock turn ends the instant it has written its reply, and the
// viewer cannot engage once the session has closed (Controller.Close makes
// engage a no-op). A keystroke whose effect is asserted is therefore sent
// only to a session the test has PROVED is live and reading: the mock runs
// its interactive echo (CTXLOOM_MOCK_ECHO_STDIN=1), the test types a
// sentinel line and waits for the engine to echo it, and the session ends
// only when the test types "quit". Pre-queuing keys and hoping the stdin pump
// reads them before the turn ends is the race this replaces: under load the
// turn ended first and the overlay never drew.

const ptyRows, ptyCols = 24, 80

// ptyRunTimeout is generous for CI: the plugin subprocess spawn (a real
// `ctxloom serve` re-exec + go-plugin handshake) alone has been observed to
// take over a second under load.
const ptyRunTimeout = 20 * time.Second

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

// TestRunPTY_SurroundBarPaints proves the surround bar establishes its
// protected bottom row and paints its status line on a real pty tall enough
// to reserve one (surround.go's reserveActive: rows >= 6) — no viewer
// interaction, just the plain interactive run.
func TestRunPTY_SurroundBarPaints(t *testing.T) {
	env := setupPTYTestEnv(t)

	sess, err := env.RunPTY(ptyCols, ptyRows, nil, "run", "-f", "viewer-fragment", "hello from the bar test")
	require.NoError(t, err)
	defer sess.Close()

	// Deadline-poll for the bar's establish signature rather than waiting for
	// the whole run to finish first: DECSTBM 1;23 protects rows 1..23 (rows=24,
	// SurroundReserve=1), and its bar body carries the rendered PrefixHint
	// (CaretHint of the default ctrl-] prefix).
	established := sess.WaitForOutput(ptyRunTimeout, func(out string) bool {
		return strings.Contains(out, "\x1b[1;23r") && strings.Contains(out, "^] viewer")
	})
	require.True(t, established, "surround bar never established within %s; captured so far: %q", ptyRunTimeout, sess.Output())

	exited, waitErr := sess.Wait(ptyRunTimeout)
	require.True(t, exited, "ctxloom run did not exit within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	require.NoError(t, waitErr)
	assert.Equal(t, 0, sess.ExitCode())

	assert.Contains(t, sess.Output(), "\x1b[r\x1b[24;1H\x1b[2K",
		"process exit restores the full scroll region and clears the bar row (Surround.Restore)")
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

// typeLineAndAwaitEcho types line into the session and waits for the mock
// engine's echo of it.
func typeLineAndAwaitEcho(t *testing.T, sess *testenv.PTYSession, line string) {
	t.Helper()
	_, err := sess.Write([]byte(line + "\n"))
	require.NoError(t, err)
	echoed := sess.WaitForOutput(ptyRunTimeout, func(out string) bool { return strings.Contains(out, "mock echo: "+line) })
	require.True(t, echoed, "the engine never echoed %q within %s; captured so far: %q", line, ptyRunTimeout, sess.Output())
}

// quitAndAwaitCleanExit ends the held mock session and requires a clean exit.
func quitAndAwaitCleanExit(t *testing.T, sess *testenv.PTYSession) {
	t.Helper()
	_, err := sess.Write([]byte("quit\n"))
	require.NoError(t, err)
	exited, waitErr := sess.Wait(ptyRunTimeout)
	require.True(t, exited, "ctxloom run did not exit within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	require.NoError(t, waitErr)
	assert.Equal(t, 0, sess.ExitCode())
}

// TestRunPTY_CtrlBracketEngagesOverlay drives the Ctrl-] viewer prefix
// through the real binary against a live session: the engage signature
// fires (the Controller's hold + alternate-screen + region handoff), the
// REAL bubbletea overlay renders its roster panel (the genuine tui.NewOverlay
// wiring from run_terminal_ui.go, not a stub), 'q' releases it back to the
// engine's screen, keystrokes reach the engine again, and the process exit
// leaves the scroll region restored.
func TestRunPTY_CtrlBracketEngagesOverlay(t *testing.T) {
	sess := startLiveViewerSession(t, "-f", "viewer-fragment", "hello from the engage test")

	_, err := sess.Write([]byte{0x1d})
	require.NoError(t, err)
	engaged := sess.WaitForOutput(ptyRunTimeout, func(out string) bool {
		return strings.Contains(out, "\x1b[?1049h\x1b[r") && strings.Contains(out, "j/k move")
	})
	require.True(t, engaged, "overlay never engaged/rendered within %s; captured so far: %q", ptyRunTimeout, sess.Output())

	_, err = sess.Write([]byte{'q'})
	require.NoError(t, err)
	released := sess.WaitForOutput(ptyRunTimeout, func(out string) bool {
		_, after, _ := strings.Cut(out, "j/k move")
		return strings.Contains(after, "\x1b[?1049l\x1b7")
	})
	require.True(t, released, "'q' never returned the screen to the engine within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	typeLineAndAwaitEcho(t, sess, "after-release")

	quitAndAwaitCleanExit(t, sess)
	assert.Contains(t, sess.Output(), "\x1b[r\x1b[24;1H\x1b[2K",
		"process exit restores the full scroll region and clears the bar row")
}

// TestRunPTY_PlainTerminalNeverEngages proves --plain-terminal opts a pty
// session out of the whole observation layer: a Ctrl-] typed into a live
// session reaches the engine as a literal byte instead of engaging a viewer,
// and no bar/region/engage signature ever appears (run.go only wraps the
// seams with setupTerminalUI when !runPlainTerminal).
func TestRunPTY_PlainTerminalNeverEngages(t *testing.T) {
	sess := startLiveViewerSession(t, "--plain-terminal", "-f", "viewer-fragment", "hello from the plain-terminal test")

	typeLineAndAwaitEcho(t, sess, "\x1dliteral-prefix")

	quitAndAwaitCleanExit(t, sess)
	out := sess.Output()
	assert.NotContains(t, out, "\x1b[1;", "--plain-terminal never establishes the surround's protected region")
	assert.NotContains(t, out, "^] viewer", "--plain-terminal never paints the bar")
	assert.NotContains(t, out, "\x1b[?1049h\x1b[r", "--plain-terminal never wires the prefix interceptor, so Ctrl-] never engages")
}
