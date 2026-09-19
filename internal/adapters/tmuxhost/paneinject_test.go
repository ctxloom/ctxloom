package tmuxhost

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// TestPaneInjector_UnmeasuredTargetIsRefusedWithNothingWritten pins the
// engine+surface allowlist, and it asserts the EFFECT rather than the error.
//
// The hazard this gate exists for is silent: `paste-buffer -p` brackets only
// for an application that enabled bracketed-paste mode, and for one that did
// not, tmux emits the text RAW instead of failing. So a regression here does
// not surface as an error anywhere — it surfaces as literal escape garbage in
// some engine's prompt, days later. Asserting only that Inject returned an
// error would keep passing if the refusal came AFTER the paste had already
// been sent, which is the one ordering that matters.
//
// The pane therefore runs a real `cat` and the test reads what it received:
// the bytes must never have left.
//
// The ZERO VALUES are covered alongside the named cases on purpose, and are
// the likelier regression. A named unmeasured engine only appears when someone
// deliberately runs one; an empty field appears whenever a caller forgets to
// set it, which for a struct literal is the DEFAULT state. Treating "" as "no
// restriction" would turn every forgetful caller into a blind paste.
//
// The MEASURED-ENGINE-ON-AN-UNMEASURED-SURFACE case is the reason this gate is
// keyed on a pair at all: the same engine runs as a TUI and as a JSON-RPC ACP
// adapter, so an allowlist keyed on the engine name alone would admit a pane
// running the adapter on a measurement taken against the TUI, and paste prose
// into a protocol stream. That subtest is what keeps the surface dimension
// honest — delete it and the pair collapses back to an engine check that
// still passes every other case here.
func TestPaneInjector_UnmeasuredTargetIsRefusedWithNothingWritten(t *testing.T) {
	for _, tc := range []struct {
		name, harp, engine, wantIn string
		surface                    agent.CLISurface
	}{
		{name: "a named engine nobody measured", harp: "mu",
			engine: "some-other-engine", surface: agent.CLISurfaceInteractive,
			wantIn: "some-other-engine"},
		{name: "the zero-value engine, i.e. a caller that forgot", harp: "nu",
			engine: "", surface: agent.CLISurfaceInteractive,
			wantIn: `engine ""`},
		{name: "a measured engine on its ACP surface, which is not a TUI", harp: "xi",
			engine: "claude-code", surface: agent.CLISurface("acp"),
			wantIn: "acp"},
		{name: "a measured engine on a surface nobody measured", harp: "omicron",
			engine: "claude-code", surface: agent.CLISurfaceOneshot,
			wantIn: "oneshot"},
		{name: "the zero-value surface, i.e. a caller that forgot", harp: "pi",
			engine: "claude-code", surface: "",
			wantIn: `surface ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPaneHostForTest(t)
			ctx := context.Background()

			require.NoError(t, h.Start(ctx, tc.harp, PaneSpec{
				Command: "sh",
				Args: []string{"-c",
					`printf '\033[?2004h'; stty raw -echo; printf READY-8b04; exec cat -v`},
				Engine: tc.engine, Surface: tc.surface,
			}))
			t.Cleanup(func() { _ = h.Stop(context.Background(), tc.harp) })

			var rec recorder
			detach, err := h.Attach(tc.harp, &rec)
			require.NoError(t, err)
			defer detach()
			require.Eventually(t, func() bool { return strings.Contains(rec.text(), "READY-8b04") },
				5*time.Second, 25*time.Millisecond, "pane never came up")

			err = h.Injector().Inject(ctx, tc.harp, "MUST-NOT-ARRIVE-2d71", true)

			require.Error(t, err, "an unmeasured engine+surface pair must be refused, not pasted into blind")
			assert.ErrorIs(t, err, ErrPasteUnmeasured,
				"the refusal must be typed so a caller can tell it from a tmux failure")
			assert.Contains(t, err.Error(), tc.wantIn,
				"the refusal must say what it refused")

			// The effect. Give the paste every chance to show up before
			// concluding it did not: a refusal that merely returned late
			// would otherwise pass here.
			time.Sleep(250 * time.Millisecond)
			got := rec.text()
			assert.NotContains(t, got, "MUST-NOT-ARRIVE-2d71",
				"refused text reached the pane anyway: %q", got)
			assert.NotContains(t, got, "^[[200~",
				"a paste was sent despite the refusal: %q", got)

			// And no server-wide buffer may be left staged for something
			// else to read.
			out, err := h.terms.runner.Run(ctx, "list-buffers")
			require.NoError(t, err)
			assert.NotContains(t, out, "ctxloom-",
				"refusal staged a paste buffer it never used: %q", out)
		})
	}
}

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
		// The stand-in enables bracketed paste for real (the printf above),
		// which is the property that puts claude on the allowlist.
		Engine: "claude-code", Surface: agent.CLISurfaceInteractive,
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
		Command: "sh", Args: []string{"-c", "exec cat"}, Engine: "claude-code", Surface: agent.CLISurfaceInteractive,
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

// stagingCapturingRunner records the CONTENT of the paste staging file at the
// moment `load-buffer` names it. The file cannot be read after Inject returns
// -- a deferred Remove deletes it -- so capturing it mid-call is the only way
// to see what tmux would actually have loaded.
type stagingCapturingRunner struct {
	*fakeTmuxRunner
	staged    string
	stagedSet bool
	stagedErr error
}

func (r *stagingCapturingRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "load-buffer" {
		b, err := os.ReadFile(args[len(args)-1])
		r.staged, r.stagedSet, r.stagedErr = string(b), true, err
	}
	return r.fakeTmuxRunner.Run(ctx, args...)
}

// TestPaneInjector_StagesExactlyTheTextForLoadBuffer covers the staging write
// itself, which until now NOTHING did.
//
// Found by mutation during close-out: replacing the staged bytes with a
// constant ("MUTANT") left internal/adapters/tmuxhost fully green. Every existing test
// that reaches the write drives a real tmux binary, and those skip wherever
// tmux is off PATH -- as it was in the agent container this was found in,
// whose image predated the base image gaining tmux (5dd35728). The host and
// the current image both have it, so those tests do run there; the point is
// that a gate CAN be green with them all skipped. Worse, the one
// PaneInjector test that does still run without tmux,
// TestPaneInjector_UnmeasuredTargetIsRefusedWithNothingWritten, reports
// "--- PASS" while ALL FIVE of its subtests skip, and it returns at the
// refusal anyway, before any write happens.
//
// This test is deliberately fake-backed so it runs everywhere. It does not
// replace the real-tmux tests -- those prove tmux ACCEPTS the argv, which a
// fake cannot -- it proves the bytes handed to tmux are the caller's, which
// the real ones never isolated.
func TestPaneInjector_StagesExactlyTheTextForLoadBuffer(t *testing.T) {
	const text = "first line\nsecond line\n\twith a tab"

	r := &stagingCapturingRunner{fakeTmuxRunner: newFakeTmuxRunner()}
	h := NewPaneHost(r, t.TempDir())
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "h1", PaneSpec{
		Command: "sh",
		Engine:  "claude-code", Surface: agent.CLISurfaceInteractive,
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "h1") })

	require.NoError(t, h.Injector().Inject(ctx, "h1", text, false))

	require.True(t, r.stagedSet, "Inject must stage the paste through load-buffer")
	require.NoError(t, r.stagedErr, "the staged file must exist when load-buffer names it")
	assert.Equal(t, text, r.staged,
		"tmux must be handed the caller's exact bytes, not a re-encoding of them")
}

// TestPaneInjector_StagingFileIsRemovedAfterThePaste pins the cleanup half.
// The staged file holds whatever was injected, so a leaked one leaves that
// text readable on disk after the paste is done.
func TestPaneInjector_StagingFileIsRemovedAfterThePaste(t *testing.T) {
	r := &stagingCapturingRunner{fakeTmuxRunner: newFakeTmuxRunner()}
	dir := t.TempDir()
	h := NewPaneHost(r, dir)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "h1", PaneSpec{
		Command: "sh",
		Engine:  "claude-code", Surface: agent.CLISurfaceInteractive,
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "h1") })

	require.NoError(t, h.Injector().Inject(ctx, "h1", "secret text", false))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "ctxloom-paste-",
			"the staging file must not outlive the paste: %q", e.Name())
	}
}

// TestPaneInjector_AdmitsOnlyTheEngineNameTheLauncherSends pins the allowlist
// to the spelling that reaches it in production and to nothing else.
//
// The chain that decides the key: agent.BaseBackend.run stamps
// LaunchSpec.Engine from b.name, the claude backend registers that name as
// "claude-code" (agent.NewBaseBackend("claude-code", ...) in
// internal/claude/claudecode.go), and backends.launchInPane copies
// LaunchSpec.Engine into PaneSpec.Engine verbatim. So a real interactive
// claude run arrives here as "claude-code", and that is the ONE spelling the
// allowlist admits: an engine has one name and no alias resolves another
// spelling to it, so "claude" is an unmeasured engine and is refused with
// nothing written.
//
// The admitted case asserts the EFFECT, not the absence of an error: the
// program in the pane only echoes once `read` returns, so a refusal — or a
// paste that never actuated — produces nothing here.
func TestPaneInjector_AdmitsOnlyTheEngineNameTheLauncherSends(t *testing.T) {
	h := newPaneHostForTest(t)
	ctx := context.Background()

	require.NoError(t, h.Start(ctx, "canon", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo PASTED-[$x]; sleep 30"},
		Engine: "claude-code", Surface: agent.CLISurfaceInteractive,
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "canon") })

	var r recorder
	detach, err := h.Attach("canon", &r)
	require.NoError(t, err)
	defer detach()

	waitFor(t, "the pane must exist before injection", func() bool {
		_, perr := h.pane("canon")
		return perr == nil
	})

	require.NoError(t, h.Injector().Inject(ctx, "canon", "canon-9f31", true),
		"the launcher's registered spelling is the measured claude TUI and must be admitted")

	waitFor(t, "the injected paste must reach the program AND be submitted",
		func() bool { return strings.Contains(r.text(), "PASTED-[canon-9f31]") })

	require.NoError(t, h.Start(ctx, "short", PaneSpec{
		Command: "sh", Args: []string{"-c", "read x; echo PASTED-[$x]; sleep 30"},
		Engine: "claude", Surface: agent.CLISurfaceInteractive,
	}))
	t.Cleanup(func() { _ = h.Stop(context.Background(), "short") })
	err = h.Injector().Inject(ctx, "short", "short-2c77", true)
	require.ErrorIs(t, err, ErrPasteUnmeasured, "a retired short spelling is not the measured engine")
	assert.Contains(t, err.Error(), `engine "claude"`)
}
