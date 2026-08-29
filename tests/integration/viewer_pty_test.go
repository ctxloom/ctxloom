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
// observation layer (internal/termui's surround bar + prefix-key viewer)
// engages through the REAL CLI dispatch and process lifecycle — the one seam
// F1's in-process harnesses (internal/termui/overlay_composition_test.go,
// internal/cli/tui/overlay_test.go) cannot reach, since those construct
// termui.Controller/tui.Overlay directly rather than through `ctxloom run`.
//
// Gotcha for anyone extending this file: SetupMockLM writes config.yaml at
// ctxloomconfig.CurrentConfigVersion, so loading it never triggers the
// interactive "rewrite to the current schema?" confirmation. A config
// version behind CurrentConfigVersion WOULD trigger it the moment BOTH
// stdin and stdout are a real tty (internal/cli/run.go's confirmUpgrade /
// isInteractiveTerminal) — which a pty always is — hanging every RunPTY
// invocation below on an unanswered prompt. If MockLM ever falls behind the
// schema again, pass -y (runAssumeYes) to auto-commit the upgrade rather
// than reintroducing the hang.
//
// Timing note (why engage/quit keys are pre-queued, not sent reactively):
// the mock backend's Execute (internal/lm/backends/mock.go) writes its
// response and returns immediately — there is no real interactive engine
// session to hold the process open. Empirically (repeated local runs,
// serially and under 8-way parallel load, all with a fresh env per run) the
// ENTIRE hot window from the surround bar's first paint to process exit is
// on the order of 10ms, and successive engine writes inside it are often
// delivered to a reading test process as one coalesced read — by the time a
// reactive poll observes ANY signature, the whole lifecycle has frequently
// already completed. Bytes written to the pty before the child even starts
// are instead queued by the kernel and delivered whole to the interceptor's
// first Read once the child puts its tty in raw mode, which happens
// deterministically (confirmed over dozens of runs): this is why engage is
// pre-queued immediately after Start rather than triggered after polling for
// the bar to appear. A 'q' pre-queued alongside it is still delivered and
// processed by the real overlay (the roster panel reliably renders first),
// but empirically NEVER wins the race to produce the explicit user-release
// atomic sequence (panel-clear + resume + DECRC) ahead of the natural
// process-exit path's own Controller.Close-triggered abort-and-restore
// (0/15 in a local sample) — both paths converge on the same restored
// terminal, so this file asserts that convergent, always-true outcome
// (region restored on exit) rather than which path produced it. See the F2
// completion report for the full reasoning; a production-side hold-open
// affordance would be needed to test the explicit-release ordering
// deterministically at this level, which is out of scope for a test-only
// change.

const ptyRows, ptyCols = 24, 80

// ptyRunTimeout is generous for CI: the plugin subprocess spawn (a real
// `ctxloom serve` re-exec + go-plugin handshake) alone has been observed to
// take over a second under load.
//
// KNOWN FLAKY UNDER HEAVY CONCURRENT LOAD, recorded rather than smoothed.
// TestRunPTY_CtrlBracketEngagesOverlay's engage poll has been observed to
// exhaust this budget and fail on `require.True(t, engaged, ...)` while the
// same commit passed the same gate twice with the box otherwise idle. The
// load that produced it was heavier than the 8-way parallel `go test` the
// timing note above was validated against: a full acceptance suite (itself
// spawning binaries per scenario) running concurrently with a separate agent
// build. The note's load claim is therefore accurate for the case it names
// and does NOT extend to this one.
//
// The mechanism is BUDGET EXHAUSTION, not a lost race, and it is measured
// rather than inferred: the failing run burned exactly 20.05s — the whole
// deadline — without reaching the engage signature, while three isolated
// runs of the same commit on an idle box finished the ENTIRE package in
// 22.0s, 20.1s and 19.8s. So on a saturated machine this one test alone can
// consume the budget that normally covers every test here.
//
// DO NOT RAISE THIS TIMEOUT TO SILENCE IT. A threshold tuned until a gate
// stops complaining measures nothing afterwards, and this one is load-bearing:
// it is the only binary-level proof that the viewer engages through real CLI
// dispatch. The correct fix is to stop running saturating work concurrently
// with this suite, or to make the engage signal observable without a wall
// clock. Serialize the gate before believing a red here.
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

// TestRunPTY_CtrlBracketEngagesOverlay drives the Ctrl-] viewer prefix
// through the real binary: the engage signature fires (the Controller's
// synchronous hold+cursor-save+region-handoff, controller.go's engage), the
// REAL bubbletea overlay Program renders its roster panel (proving this is
// the genuine tui.NewOverlay wiring from run_terminal_ui.go, not a stub), a
// 'q' is sent (the ordinary release key), and the process exit leaves the
// scroll region restored. See the file-level comment for why the explicit
// atomic-release ordering itself isn't independently asserted here.
func TestRunPTY_CtrlBracketEngagesOverlay(t *testing.T) {
	env := setupPTYTestEnv(t)

	sess, err := env.RunPTY(ptyCols, ptyRows, nil, "run", "-f", "viewer-fragment", "hello from the engage test")
	require.NoError(t, err)
	defer sess.Close()

	// Pre-queued immediately: the kernel holds these bytes until the child
	// puts its tty in raw mode and the interceptor's first Read fires — see
	// the file-level timing note. 'q' rides along as the ordinary release key;
	// see the file-level comment for why its exact effect isn't independently
	// asserted.
	_, err = sess.Write([]byte{0x1d, 'j', 'q'})
	require.NoError(t, err)

	// Deadline-poll for the engage signature (DECSC + bare region-reset, the
	// Controller's synchronous engage handoff) and the real overlay's roster
	// panel hint line — proving the actual bubbletea Program rendered, not a
	// stub — rather than waiting for the whole run to finish first.
	engaged := sess.WaitForOutput(ptyRunTimeout, func(out string) bool {
		return strings.Contains(out, "\x1b7\x1b[r") && strings.Contains(out, "j/k move")
	})
	require.True(t, engaged, "overlay never engaged/rendered within %s; captured so far: %q", ptyRunTimeout, sess.Output())

	exited, waitErr := sess.Wait(ptyRunTimeout)
	require.True(t, exited, "ctxloom run did not exit within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	require.NoError(t, waitErr)
	assert.Equal(t, 0, sess.ExitCode())

	assert.Contains(t, sess.Output(), "\x1b[r\x1b[24;1H\x1b[2K",
		"process exit restores the full scroll region and clears the bar row")
}

// TestRunPTY_PlainTerminalNeverEngages proves --plain-terminal opts a pty
// session out of the whole observation layer: even with the Ctrl-] prefix
// and a viewer key written to the pty, no bar/region/engage signature ever
// appears (run.go only wraps the seams with setupTerminalUI when
// !runPlainTerminal).
func TestRunPTY_PlainTerminalNeverEngages(t *testing.T) {
	env := setupPTYTestEnv(t)

	sess, err := env.RunPTY(ptyCols, ptyRows, nil, "run", "--plain-terminal", "-f", "viewer-fragment", "hello from the plain-terminal test")
	require.NoError(t, err)
	defer sess.Close()

	_, err = sess.Write([]byte{0x1d, 'j', 'q'})
	require.NoError(t, err)

	exited, waitErr := sess.Wait(ptyRunTimeout)
	require.True(t, exited, "ctxloom run did not exit within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	require.NoError(t, waitErr)
	assert.Equal(t, 0, sess.ExitCode())

	out := sess.Output()
	assert.NotContains(t, out, "\x1b[1;", "--plain-terminal never establishes the surround's protected region")
	assert.NotContains(t, out, "^] viewer", "--plain-terminal never paints the bar")
	assert.NotContains(t, out, "\x1b7\x1b[r", "--plain-terminal never wires the prefix interceptor, so Ctrl-] never engages")
}
