package acp

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPaneInjector_PasteArrivesBracketed is the assertion CONSTRAINT 1 rests
// on.
//
// Injection writes immediately, mid-turn, with no wait for the pane to fall
// quiet. That is only safe because the bytes arrive as a PASTE EVENT: tmux's
// -p wraps them in ESC[200~ / ESC[201~, so a TUI knows where the pasted run
// begins and ends instead of treating it as typing. Drop the -p and every
// other test in this package still passes — the text lands, the submit
// actuates, nothing looks wrong — while the property that made immediate
// writing safe is gone. So this test reads the bytes THE HOSTED PROGRAM
// RECEIVED, not the argv used to send them.
//
// The pane runs `cat -v`, which renders the escape byte as "^[", so the
// markers appear literally in the captured output. The DECSET 2004 is what
// makes tmux emit the bracketing at all: it brackets only for an application
// that asked for it.
//
// `stty raw -echo` is not cosmetic — it is what makes this test READ THE
// PROGRAM. With echo left on, the line discipline reflects the pasted bytes
// back to the pane and every assertion below passes on the ECHO alone, while
// `cat` (canonical, no newline in the paste) receives nothing and prints
// nothing. Turning echo off means the only thing that can put "^[[200~" in
// the capture is cat having read it.
//
// `cat` is exec'd and NOT wrapped in `timeout`: timeout puts its child in a
// fresh process group, which is then not the pty's foreground group, so the
// child's tty reads fail and the paste lands nowhere. The pane is bounded by
// the Stop in t.Cleanup instead.
func TestPaneInjector_PasteArrivesBracketed(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "kappa", PaneSpec{
		Command: "sh",
		Args: []string{"-c",
			`printf '\033[?2004h'; stty raw -echo; printf READY-3f8a; exec cat -v`},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "kappa") })

	var rec recorder
	detach, err := h.Attach("kappa", &rec)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "the pane must announce it has asked for bracketed paste before anything is injected",
		func() bool { return strings.Contains(rec.text(), "READY-3f8a") })

	require.NoError(t, h.Injector().Inject(ctx, "kappa", "PASTED-9d21", false))

	defer func() { t.Logf("PANE OUTPUT: %q", rec.text()) }()
	waitFor(t, "the injected text must reach the hosted program",
		func() bool { return strings.Contains(rec.text(), "PASTED-9d21") })
	// Wait for the closing marker specifically: it is the last thing written,
	// so seeing it means the whole event was delivered.
	waitFor(t, "the paste must be delivered bracketed (ESC[201~), not as bare keystrokes",
		func() bool { return strings.Contains(rec.text(), "^[[201~") })

	got := rec.text()
	assert.Contains(t, got, "^[[200~",
		"the paste must open with the bracketed-paste start marker")
	start := strings.Index(got, "^[[200~")
	end := strings.Index(got, "^[[201~")
	body := strings.Index(got, "PASTED-9d21")
	assert.Greater(t, body, start, "the text must arrive INSIDE the paste, after the start marker")
	assert.Greater(t, end, body, "the paste must be closed AFTER the text, not before it")
}

// TestPaneInjector_PasteLeavesNoBufferBehind pins the -d in paste-buffer.
//
// tmux buffers are SERVER-WIDE and outlive the paste: anything that can talk
// to the same tmux server can list them and read them back. An injected
// instruction left sitting in a named buffer is the pasted text still
// readable long after it was delivered. Dropping -d changes nothing anyone
// can see in the pane, which is exactly why it needs an assertion of its own.
func TestPaneInjector_PasteLeavesNoBufferBehind(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "lambda", PaneSpec{
		Command: "sh", Args: []string{"-c", "exec cat"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "lambda") })

	require.NoError(t, h.Injector().Inject(ctx, "lambda", "SECRET-4c60", false))

	out, err := h.terms.runner.Run(ctx, "list-buffers")
	require.NoError(t, err)
	assert.NotContains(t, out, "ctxloom-",
		"the paste buffer must not outlive the paste: %q", out)
}

// TestPaneInjector_UnknownHarpIsRefused: a run with no live pane has nowhere
// for an instruction to land. Returning nil would report delivery for text
// that reached nobody — the "exit 0, zero bytes written" failure in its
// purest form, and the one that would let a silently-dropped injection look
// like a delivered one.
func TestPaneInjector_UnknownHarpIsRefused(t *testing.T) {
	h := newPaneHostForTest(t)

	err := h.Injector().Inject(context.Background(), "nobody-here", "text", true)
	require.Error(t, err, "injecting into a run with no pane must fail, not report success")
	assert.ErrorIs(t, err, ErrNoPane)
}
