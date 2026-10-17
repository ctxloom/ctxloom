package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
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
// skipped) still hands the bar and the overlay a working port: an empty
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
	assert.Empty(t, rows, "no coordinator hosted and no indexed sessions: an empty roster, not an error")
}

func TestTerminalUIPort_NoCoordinatorControlIsNotInjectable(t *testing.T) {
	_, err := degradedSessionPort(t, "/irrelevant").Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: "some-harp", Body: "hello"})
	require.ErrorIs(t, err, coord.ErrNotInjectable,
		"no coordinator hosted: Control degrades to the typed refusal, not a nil-pointer panic")
}

func TestTerminalUIPort_NoCoordinatorHasNoApprovalSource(t *testing.T) {
	assert.Nil(t, degradedSessionPort(t, "/irrelevant").Approvals(), "no coordinator hosted: no approval source — never a typed nil the view would call")
}

func TestSurroundRoster_NoCoordinatorIsEmptyNotError(t *testing.T) {
	rows, err := surroundRoster(context.Background(), degradedSessionPort(t, "/irrelevant"))
	require.NoError(t, err)
	assert.Empty(t, rows, "the bar shows just this session, not an error, when no coordinator is hosted")
}

// TestSurroundRows_CarryTheHoldInTheOverlaysWords: the bar's rows take a held
// child's hold as the overlay words it (tui.HoldLabel), and an unheld child
// carries none.
func TestSurroundRows_CarryTheHoldInTheOverlaysWords(t *testing.T) {
	hold := &coord.RunHold{Kind: "rate_limited", Until: time.Date(2026, 10, 1, 17, 30, 0, 0, time.UTC)}
	rows := surroundRows([]coord.RunInfo{
		{Agent: &coord.AgentIdentity{AgentID: "held-kid"}, Phase: coord.StateIdle, LastEventAt: time.Unix(7, 0), Hold: hold},
		{Agent: &coord.AgentIdentity{AgentID: "free-kid"}, Phase: coord.StateExecuting},
		{RunID: "no-identity", Phase: coord.StateExecuting},
	})
	require.Len(t, rows, 2, "a run with no identity names no harp, so makes no row")
	assert.Equal(t, termui.RosterEntry{Harp: "held-kid", State: coord.StateIdle, LastActivityUnix: 7, Hold: "held: rate limited until 17:30 UTC"}, rows[0])
	assert.Zero(t, rows[1].LastActivityUnix, "no recorded activity is 0, never the zero time's seconds")
	assert.Equal(t, tui.HoldLabel(hold), rows[0].Hold, "one wording, overlay and bar")
	assert.Empty(t, rows[1].Hold)
}

// TestSurroundRows_CarryTheUnreadCount: the bar's rows take a child's unread
// mail count from the coordinator's roster unchanged.
func TestSurroundRows_CarryTheUnreadCount(t *testing.T) {
	rows := surroundRows([]coord.RunInfo{{Agent: &coord.AgentIdentity{AgentID: "kid"}, Phase: coord.StateExecuting, Unread: 2}})
	require.Len(t, rows, 1)
	assert.Equal(t, 2, rows[0].Unread)
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
	restore := divertRunnerDiagnostics(func(k string) string {
		if k == sessions.EnvDiagnosticsLog {
			return log
		}
		return ""
	})
	clidiag.Warn("ctxloom", "runner-side warning")
	restore()

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
