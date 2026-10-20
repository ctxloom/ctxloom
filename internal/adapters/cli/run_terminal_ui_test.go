package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

func TestValidateTerminalUIConfig_BadKeyIsFatalFinding(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})

	mark := strictness.Checkpoint()
	cfg := config.NewFixture(config.Fixture{UI: config.UIConfig{PrefixKey: "ctrl-["}}) // ESC: rejected
	validateTerminalUIConfig(cfg)

	found := strictness.Since(mark)
	require.Len(t, found, 1, "an invalid prefix key must be a collected startup finding")
	assert.Equal(t, report.KindConfig, found[0].Kind)
	assert.Contains(t, found[0].Text, "ui.prefix_key")
}

func TestValidateTerminalUIConfig_DefaultAndValidKeysPass(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	mark := strictness.Checkpoint()
	validateTerminalUIConfig(&config.Config{}) // default ctrl-]
	validateTerminalUIConfig(config.NewFixture(config.Fixture{UI: config.UIConfig{PrefixKey: "ctrl-t"}}))
	assert.Empty(t, strictness.Since(mark))
}

// A run with no hosted coordinator (coordinator startup failed, or was
// skipped) still hands the split view a working port: an empty
// roster (never an error), the typed ErrNotInjectable rather than a
// nil-pointer panic on Control, and no approval queue.

// degradedSessionPort is the frontend port of a session that hosts no
// coordinator, composed as `ctxloom run` composes it.
func degradedSessionPort(t *testing.T, workDir string) frontend.Port {
	t.Helper()
	return (&runState{workDir: workDir, activeHarp: "self-harp"}).frontendPort()
}

func TestTerminalUIPort_NoCoordinatorRosterDegradesToEmptyNotError(t *testing.T) {
	home := testsupport.Isolate(t)
	workDir := home + "/proj"
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	rows, err := tui.LoadRoster(context.Background(), degradedSessionPort(t, workDir), "self-harp")
	require.NoError(t, err)
	require.Len(t, rows, 1, "no coordinator hosted and no indexed sessions: the session's own row alone, not an error")
	assert.Equal(t, "self-harp", rows[0].Harp)
}

func TestTerminalUIPort_NoCoordinatorControlIsNotInjectable(t *testing.T) {
	_, err := degradedSessionPort(t, "/irrelevant").Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: "some-harp", Body: "hello"})
	require.ErrorIs(t, err, coord.ErrNotInjectable,
		"no coordinator hosted: Control degrades to the typed refusal, not a nil-pointer panic")
}

func TestTerminalUIPort_NoCoordinatorHasNoApprovalSource(t *testing.T) {
	assert.Nil(t, degradedSessionPort(t, "/irrelevant").Approvals(), "no coordinator hosted: no approval source — never a typed nil the view would call")
}

// Closing the split view leaves, after the engine's final frame, the line
// that says where the whole session is: the main screen keeps only the last
// screen, the session's transcript keeps the rest.
func TestSetupSplitView_CloseNamesTheSessionTranscript(t *testing.T) {
	home := testsupport.Isolate(t)
	workDir := home + "/proj"
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, feed := io.Pipe()
	defer func() { _ = feed.Close() }()
	var out bytes.Buffer

	view := setupSplitView(ctx, &config.Config{}, degradedSessionPort(t, workDir), terminalUIIdentity{Harp: "self-harp"}, paneTerm{}, splitIO{in: in, out: &out})
	require.NotNil(t, view)
	view.Close()

	transcript, err := paths.HarpCanonicalTranscriptPath("self-harp")
	require.NoError(t, err)
	assert.True(t, bytes.HasSuffix(out.Bytes(), []byte(fmt.Sprintf(SplitTranscriptLine, transcript)+"\r\n")),
		"the transcript line is the last thing written: %q", out.String())
}

// TestRedirectDiagnosticsForTUI_AnnouncesTheOutcome pins both halves of the
// handover contract. On success the user is told where the diagnostics went;
// on EVERY failure the user is told they did NOT go there, because the
// consequence is real and otherwise invisible: warnings keep writing to the
// terminal the TUI is about to paint on, so the display corrupts mid-frame and
// nothing anywhere explains it. The announcement rides the announce writer, not
// clidiag — it happens before the handover, and routing it through the sink
// this function is failing to redirect would be circular.
func TestRedirectDiagnosticsForTUI_AnnouncesTheOutcome(t *testing.T) {
	t.Run("success names the log path", func(t *testing.T) {
		testsupport.Isolate(t)
		var announce bytes.Buffer
		restore := redirectDiagnosticsForTUI("plump-loose-sash", &announce)
		t.Cleanup(restore)
		assert.Contains(t, announce.String(), paths.DiagnosticsLogFileName)
	})

	t.Run("no harp is announced, not silently skipped", func(t *testing.T) {
		testsupport.Isolate(t)
		var announce bytes.Buffer
		restore := redirectDiagnosticsForTUI("", &announce)
		t.Cleanup(restore)
		assert.NotEmpty(t, announce.String(),
			"a session whose diagnostics were NOT diverted must say so — they will land on the terminal the TUI paints")
	})

	t.Run("an unusable session dir is announced", func(t *testing.T) {
		testsupport.Isolate(t)
		// A regular file where the harp dir must be: MkdirAll cannot proceed.
		dir, err := paths.HarpDir("swift-amber-falcon")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
		require.NoError(t, os.WriteFile(dir, []byte("not a dir"), 0o644))

		var announce bytes.Buffer
		restore := redirectDiagnosticsForTUI("swift-amber-falcon", &announce)
		t.Cleanup(restore)
		assert.NotEmpty(t, announce.String(),
			"a diagnostics log that could not be created must be reported, not returned as an indistinguishable no-op")
	})

	t.Run("an invalid harp is announced", func(t *testing.T) {
		testsupport.Isolate(t)
		var announce bytes.Buffer
		restore := redirectDiagnosticsForTUI("Not A Harp/../escape", &announce)
		t.Cleanup(restore)
		assert.NotEmpty(t, announce.String(),
			"a harp that does not resolve to a session dir must be reported")
	})
}

// TestDivertRunnerDiagnostics_WarningsLandInTheNamedLog: an interactive
// runner's stderr is the engine's pty, so a warning printed there is painted
// into the middle of the engine's screen. Handed the session's log, the
// runner writes its warnings there instead.
func TestDivertRunnerDiagnostics_WarningsLandInTheNamedLog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "diagnostics.log")
	env := map[string]string{sessions.EnvDiagnosticsLog: log}
	restore := divertRunnerDiagnostics(
		func(k string) string { return env[k] },
		func(k string) error { delete(env, k); return nil })
	clidiag.Warn("ctxloom", "runner-side warning")
	restore()

	assert.NotContains(t, env, sessions.EnvDiagnosticsLog,
		"the path is consumed: an engine built from this environment must not inherit it")
	got, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(got), "runner-side warning")
}

// TestRunnerTerminalEnv_ForwardsTheLogOnlyWhenTheUIOwnsTheTerminal: the
// runner's diagnostics follow this process's own — diverted exactly when
// prepareSessionIO diverts them (a real tty, no --plain-terminal) — and the
// coordinator's env map is never written through.
func TestRunnerTerminalEnv_ForwardsTheLogOnlyWhenTheUIOwnsTheTerminal(t *testing.T) {
	testsupport.Isolate(t)
	oldTerm, oldPlain := termIsTerminal, runPlainTerminal
	t.Cleanup(func() { termIsTerminal, runPlainTerminal = oldTerm, oldPlain })
	st := &runState{activeHarp: "self-harp"}
	spawn := map[string]string{"K": "V"}
	want, err := diagnosticsLogPath("self-harp")
	require.NoError(t, err)

	termIsTerminal, runPlainTerminal = func(int) bool { return true }, false
	env := st.runnerTerminalEnv(spawn)
	assert.Equal(t, want, env[sessions.EnvDiagnosticsLog])
	assert.Equal(t, "V", env["K"])
	assert.NotContains(t, spawn, sessions.EnvDiagnosticsLog, "the caller's map must not be written through")

	runPlainTerminal = true
	assert.NotContains(t, st.runnerTerminalEnv(spawn), sessions.EnvDiagnosticsLog, "--plain-terminal keeps diagnostics on the terminal")

	termIsTerminal, runPlainTerminal = func(int) bool { return false }, false
	assert.NotContains(t, st.runnerTerminalEnv(spawn), sessions.EnvDiagnosticsLog, "no tty, no terminal UI, nothing to divert from")
}

// A harp no session dir can be named for leaves no epilogue rather than a
// line naming no path.
func TestSplitEpilogue_NamesTheTranscriptOrNothing(t *testing.T) {
	testsupport.Isolate(t)
	transcript, err := paths.HarpCanonicalTranscriptPath("self-harp")
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf(SplitTranscriptLine, transcript), splitEpilogue("self-harp"))
	assert.Empty(t, splitEpilogue("../not-a-harp"))
}

// A split view that cannot start is none, and says why: the session runs on
// the plain seams with the reason on the human's stderr.
func TestSetupSplitView_AViewThatCannotStartWarnsAndIsNone(t *testing.T) {
	for name, c := range map[string]struct {
		cfg   *config.Config
		human splitIO
	}{
		// Only under --degraded: the startup gate refuses it otherwise.
		"a prefix key it refuses": {config.NewFixture(config.Fixture{UI: config.UIConfig{PrefixKey: "ctrl-["}}), splitIO{in: bytes.NewReader(nil), out: io.Discard}},
		"no output terminal":      {&config.Config{}, splitIO{in: bytes.NewReader(nil)}},
	} {
		t.Run(name, func(t *testing.T) {
			home := testsupport.Isolate(t)
			var diag bytes.Buffer
			restore := clidiag.SetSink(&diag)
			defer restore()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			view := setupSplitView(ctx, c.cfg, degradedSessionPort(t, home), terminalUIIdentity{Harp: "self-harp"}, paneTerm{}, c.human)
			assert.Nil(t, view)
			assert.Contains(t, diag.String(), noticePrefix(splitViewDisabledWarning))
		})
	}
}

// A desktop notification is bounded: the notifier gets a live context that
// ends desktopAlertWait from now, never one already over.
func TestDesktopAlert_BoundsTheNotifier(t *testing.T) {
	host := &recordingNotifier{}
	start := time.Now()
	desktopAlert{host: host, resolve: func(string) (string, error) { return "", nil }}.Notify("t", "b")
	require.True(t, host.called)
	assert.NoError(t, host.errAtCall, "the notifier is not handed an expired context")
	require.True(t, host.hasDeadline)
	assert.WithinRange(t, host.deadline, start, time.Now().Add(desktopAlertWait))
}

type recordingNotifier struct {
	called      bool
	errAtCall   error
	deadline    time.Time
	hasDeadline bool
}

func (n *recordingNotifier) Notify(ctx context.Context, _ func(string) (string, error), _, _ string) error {
	n.called = true
	n.errAtCall = ctx.Err()
	n.deadline, n.hasDeadline = ctx.Deadline()
	return nil
}

func TestValidateTerminalUIConfig_BadListWindowIsFatalFinding(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	mark := strictness.Checkpoint()
	validateTerminalUIConfig(config.NewFixture(config.Fixture{UI: config.UIConfig{ListWindow: "soon"}}))
	found := strictness.Since(mark)
	require.Len(t, found, 1, "an invalid list window must be a collected startup finding")
	assert.Equal(t, report.KindConfig, found[0].Kind)
	assert.Contains(t, found[0].Text, "ui.list_window")
	assert.Contains(t, found[0].Text, tui.ErrInvalidListWindow.Error())

	mark = strictness.Checkpoint()
	for _, ok := range []string{"", "7d", "0", "all"} {
		validateTerminalUIConfig(config.NewFixture(config.Fixture{UI: config.UIConfig{ListWindow: ok}}))
	}
	assert.Empty(t, strictness.Since(mark))
}
