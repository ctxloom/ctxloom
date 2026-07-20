package cli

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
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
