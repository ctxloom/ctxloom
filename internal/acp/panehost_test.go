package acp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive a REAL tmux, for the same reason tmux_host_test.go does: every
// claim PaneHost makes is about what tmux and the hosted process actually do
// (did the bytes reach both viewers, did the paste get typed, is the window
// still alive after a detach). An argv assertion cannot answer any of them.

// recorder is a PaneClient that accumulates what it was handed. Every
// assertion in this file reads it, so it records EFFECTS — bytes delivered
// and the close it was told about — rather than that a method was called.
type recorder struct {
	mu     sync.Mutex
	out    strings.Builder
	closed bool
	code   int32
}

func (r *recorder) Output(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.Write(p)
}

func (r *recorder) Closed(code int32, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed, r.code = true, code
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

func (r *recorder) isClosed() (bool, int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed, r.code
}

// newPaneHostForTest builds a PaneHost on a socket private to this test and
// registers teardown, mirroring realTmux's discipline.
func newPaneHostForTest(t *testing.T) *PaneHost {
	t.Helper()
	runner, _ := realTmux(t)
	return NewPaneHost(runner, t.TempDir())
}

// waitFor polls until cond holds, failing rather than hanging. Polling and
// not sleeping: these are synchronizations on another process's output.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 20*time.Second, 25*time.Millisecond, why)
}

// TestPaneHost_AttachFansOutToConcurrentViewers pins the sharing rule: two
// humans attached to one run both see it, and neither gets a private copy of
// the pane. A fanout that delivered to only the first (or last) registered
// client would look completely healthy to a single-viewer test.
func TestPaneHost_AttachFansOutToConcurrentViewers(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "alpha", PaneSpec{
		Command: "sh", Args: []string{"-c", "echo FANOUT-MARKER-7c1d; sleep 30"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "alpha") })

	var a, b recorder
	detachA, err := h.Attach("alpha", &a)
	require.NoError(t, err)
	defer detachA()
	detachB, err := h.Attach("alpha", &b)
	require.NoError(t, err)
	defer detachB()

	waitFor(t, "the first viewer must receive the pane's bytes",
		func() bool { return strings.Contains(a.text(), "FANOUT-MARKER-7c1d") })
	waitFor(t, "the SECOND viewer must receive the same bytes — output fans out, it is not handed to one client",
		func() bool { return strings.Contains(b.text(), "FANOUT-MARKER-7c1d") })
}

// TestPaneHost_DetachDoesNotKillThePane is the lifetime invariant, stated as
// the thing most likely to be broken by a plausible refactor: reference-count
// the clients and tear the pane down when the count hits zero, and this is the
// only test that goes red.
//
// It asserts on the tmux WINDOW and on a surviving viewer's bytes, not on an
// internal flag, because "the pane object still exists" is satisfied by a pane
// whose process has been killed.
func TestPaneHost_DetachDoesNotKillThePane(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	// Emits continuously, so a live pane is observable as NEW bytes after the
	// detach rather than as bytes captured before it.
	require.NoError(t, h.Start(ctx, "beta", PaneSpec{
		Command: "sh", Args: []string{"-c", "i=0; while :; do echo TICK-$i; i=$((i+1)); sleep 0.2; done"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "beta") })

	var leaving, staying recorder
	detach, err := h.Attach("beta", &leaving)
	require.NoError(t, err)
	stay, err := h.Attach("beta", &staying)
	require.NoError(t, err)
	defer stay()

	waitFor(t, "precondition: both viewers must be receiving before one leaves",
		func() bool { return strings.Contains(leaving.text(), "TICK-") && strings.Contains(staying.text(), "TICK-") })

	detach()

	// THE WINDOW is the assertion that pins "detach must never kill the pane".
	listed, lerr := h.terms.runner.Run(ctx, "list-windows", "-a", "-F", "#{session_name}:#{window_name}")
	require.NoError(t, lerr)
	p, perr := h.pane("beta")
	require.NoError(t, perr, "the pane must still be registered after a detach")
	assert.Contains(t, listed, p.term.window, "detaching a viewer must not destroy the run's tmux window")

	// And the remaining viewer must still be fed. A pane whose tail goroutine
	// was stopped by the detach would keep its window and go silent.
	before := staying.text()
	waitFor(t, "the remaining viewer must keep receiving new output after the other detached",
		func() bool { return len(staying.text()) > len(before) })

	// The departed viewer must be genuinely unsubscribed, not merely ignored.
	after := leaving.text()
	time.Sleep(600 * time.Millisecond)
	assert.Equal(t, after, leaving.text(), "a detached viewer must stop receiving bytes")
}

// TestPaneHost_InputReachesTheHostedProcess proves send-keys -H actually
// drives the program's stdin. The witness is the program's OWN echo of what
// it read, so a tmux call that succeeded without delivering anything (a wrong
// target, a keys encoding tmux accepts and drops) fails here.
func TestPaneHost_InputReachesTheHostedProcess(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "gamma", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo GOT-[$x]; sleep 30"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "gamma") })

	var r recorder
	detach, err := h.Attach("gamma", &r)
	require.NoError(t, err)
	defer detach()

	// Wait for the pane to be ready to read, then type.
	waitFor(t, "the pane must exist before input can reach it", func() bool {
		_, perr := h.pane("gamma")
		return perr == nil
	})
	require.NoError(t, h.Input(ctx, "gamma", []byte("typed-9f3a\r")))

	waitFor(t, "raw input must reach the hosted process's stdin and be read by it",
		func() bool { return strings.Contains(r.text(), "GOT-[typed-9f3a]") })
}

// TestPaneHost_InputSendsLiteralBytesNotKeyNames pins the `-H` in send-keys,
// which the test above does NOT: dropping -H there still passes, because tmux
// happens to send an unrecognised string literally.
//
// The distinction is only visible on input that COLLIDES with a tmux key name.
// Without -H, `send-keys Enter` sends the Enter KEY, so a human typing the
// five characters "Enter" submits an empty line instead — their text silently
// becomes a keypress. With -H every byte is a hex literal and no such
// vocabulary exists.
func TestPaneHost_InputSendsLiteralBytesNotKeyNames(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "eta", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo GOT-[$x]; sleep 30"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "eta") })

	var r recorder
	detach, err := h.Attach("eta", &r)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "the pane must exist before input can reach it", func() bool {
		_, perr := h.pane("eta")
		return perr == nil
	})
	// "Enter" is a tmux KEY NAME. As text it must stay five characters.
	require.NoError(t, h.Input(ctx, "eta", []byte("Enter")))
	require.NoError(t, h.Input(ctx, "eta", []byte("\r")))

	waitFor(t, "text colliding with a tmux key name must arrive as literal bytes",
		func() bool { return strings.Contains(r.text(), "GOT-[Enter]") })
	assert.NotContains(t, r.text(), "GOT-[]",
		"the literal text \"Enter\" was interpreted as the Enter key — send-keys lost -H")
}

// TestPaneHost_InjectPastesAndSubmits is the whole of Inject's contract in
// one effect: the text must land in the program AND the submit must actuate
// it. The program only echoes once `read` returns, which requires a newline
// it never received as part of the paste — so a paste that staged the text
// without submitting produces NOTHING here, which is precisely the silent
// failure the old quiet-then-type path shipped with.
func TestPaneHost_InjectPastesAndSubmits(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "delta", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo PASTED-[$x]; sleep 30"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "delta") })

	var r recorder
	detach, err := h.Attach("delta", &r)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "the pane must exist before injection", func() bool {
		_, perr := h.pane("delta")
		return perr == nil
	})
	require.NoError(t, h.Inject(ctx, "delta", "injected-5b2c", true))

	waitFor(t, "an injected paste must reach the program AND be submitted",
		func() bool { return strings.Contains(r.text(), "PASTED-[injected-5b2c]") })
}

// TestPaneHost_InjectWithoutSubmitDoesNotActuate is the negative half, and it
// is what stops the test above from passing for the wrong reason. If Inject
// ignored `submit` and always sent Enter, the test above would still be green
// — this one would not.
func TestPaneHost_InjectWithoutSubmitDoesNotActuate(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "epsilon", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo PASTED-[$x]; sleep 30"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "epsilon") })

	var r recorder
	detach, err := h.Attach("epsilon", &r)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "the pane must exist before injection", func() bool {
		_, perr := h.pane("epsilon")
		return perr == nil
	})
	require.NoError(t, h.Inject(ctx, "epsilon", "staged-1a7e", false))

	// Give the paste time to land and the shell time to have reacted if it
	// were going to. The absence being asserted is only meaningful after the
	// text has demonstrably arrived, so wait for the echo of the paste first.
	waitFor(t, "the pasted text must at least appear in the pane",
		func() bool { return strings.Contains(r.text(), "staged-1a7e") })
	time.Sleep(500 * time.Millisecond)
	assert.NotContains(t, r.text(), "PASTED-[",
		"submit=false must stage the text without actuating it")
}

// TestPaneHost_ClosedCarriesTheTrueExitCode: a viewer must learn the run
// ended, with the code it ended on. Deliberately not zero — a close path that
// hardcodes success or reports the wrapper's status cannot produce 7.
func TestPaneHost_ClosedCarriesTheTrueExitCode(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "zeta", PaneSpec{
		Command: "sh", Args: []string{"-c", "echo BYE-3e9f; exit 7"},
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "zeta") })

	var r recorder
	detach, err := h.Attach("zeta", &r)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "a viewer must be told the pane closed", func() bool {
		closed, _ := r.isClosed()
		return closed
	})
	_, code := r.isClosed()
	assert.Equal(t, int32(7), code, "the COMMAND's exit code must reach the viewer")
	assert.Contains(t, r.text(), "BYE-3e9f",
		"the pane's final bytes must be delivered before the close, not lost to the exit race")
}

// TestPaneHost_AttachUnknownHarpIsRefused: attaching to a run that is not
// hosted must fail loudly. A nil-returning attach that silently produced a
// client receiving nothing is the "succeeds without doing the thing" shape.
func TestPaneHost_AttachUnknownHarpIsRefused(t *testing.T) {
	h := newPaneHostForTest(t)
	var r recorder
	detach, err := h.Attach("nobody", &r)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoPane)
	assert.Nil(t, detach, "a refused attach must not hand back a detach")
}
