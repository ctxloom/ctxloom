package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestValidateTerminalUIConfig_BadKeyIsFatalFinding(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})

	mark := strictness.Checkpoint()
	cfg := config.NewFixture(config.Fixture{UI: config.UIConfig{PrefixKey: "ctrl-["}}) // ESC: rejected
	validateTerminalUIConfig(cfg)

	found := strictness.Since(mark)
	require.Len(t, found, 1, "an invalid prefix key must be a collected startup finding")
	assert.Equal(t, strictness.ClassConfig, found[0].Class)
	assert.Contains(t, found[0].Message, "ui.prefix_key")
}

func TestValidateTerminalUIConfig_DefaultAndValidKeysPass(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	mark := strictness.Checkpoint()
	validateTerminalUIConfig(&config.Config{}) // default ctrl-]
	validateTerminalUIConfig(config.NewFixture(config.Fixture{UI: config.UIConfig{PrefixKey: "ctrl-t"}}))
	assert.Empty(t, strictness.Since(mark))
}

// F1 deliverable 5 (opportunistic unit gaps): terminalUISources'
// nil-coordinator degradations (run_terminal_ui.go:90-144) were untested —
// a run with no hosted coordinator (coordinator startup failed, or was
// skipped) must still hand the overlay a working Sources value: an empty
// bar roster (never an error), and a typed ErrNotInjectable rather than a
// nil-pointer panic on Inject.

func TestTerminalUISources_NilCoordinatorRosterDegradesToEmptyNotError(t *testing.T) {
	home := testsupport.Isolate(t)
	workDir := home + "/proj"
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	src := terminalUISources(nil, workDir, "self-harp")
	rows, err := src.Roster(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rows, "no coordinator hosted and no indexed sessions: an empty bar roster, not an error")
}

func TestTerminalUISources_NilCoordinatorInjectIsNotInjectable(t *testing.T) {
	src := terminalUISources(nil, "/irrelevant", "self-harp")
	_, err := src.Inject("some-harp", "hello")
	require.ErrorIs(t, err, coord.ErrNotInjectable,
		"no coordinator hosted: Inject degrades to the typed refusal, not a nil-pointer panic")
}

func TestSurroundRoster_NilCoordinatorIsEmptyNotError(t *testing.T) {
	rows, err := surroundRoster(nil)
	require.NoError(t, err)
	assert.Nil(t, rows, "the bar shows just this session, not an error, when no coordinator is hosted")
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
		assert.Contains(t, announce.String(), diagnosticsLogName)
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
