package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestApplyHooksDryRunWritesNothing pins the dry-run posture against a real
// apply of the SAME request, because the assertion is only worth anything as a
// pair: "no file appeared" is trivially true in a fixture that never creates
// one, and that shape is exactly how this project's characteristic silent
// no-op hides. The control arm proves the fixture DOES produce a settings file
// when allowed to, so the dry-run arm's absence means suppression rather than
// a test that never exercised the writer.
//
// Why this exists at all: starting the MCP server applies hooks — rewriting
// the project's managed settings is what ctxloom does — and a run that merely
// wanted to inspect the resolution silently rewrote a real project's
// settings.json, dropping a configured PreToolUse hook. Dry run is the way to
// ask the question without answering it destructively.
func TestApplyHooksDryRunWritesNothing(t *testing.T) {
	const settingsPath = "/project/.claude/settings.json"

	newFixture := func(t *testing.T) (afero.Fs, func() (*config.Config, error)) {
		t.Helper()
		fs := afero.NewMemMapFs()
		loader := func() (*config.Config, error) {
			return cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
				},
			}, config.Fixture{}), nil
		}
		return fs, loader
	}

	t.Run("control: a real apply writes the settings file", func(t *testing.T) {
		fs, loader := newFixture(t)

		_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend: "claude-code",
			FS:      fs,
			Cfg:     loaded(t, loader),
			WorkDir: "/project",
		})
		require.NoError(t, err)

		exists, err := afero.Exists(fs, settingsPath)
		require.NoError(t, err)
		require.True(t, exists,
			"the control arm must actually write; without it the dry-run arm proves nothing")
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		fs, loader := newFixture(t)

		_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend: "claude-code",
			FS:      fs,
			Cfg:     loaded(t, loader),
			WorkDir: "/project",
			DryRun:  true,
		})
		require.NoError(t, err, "a dry run resolves in full and must not error")

		exists, err := afero.Exists(fs, settingsPath)
		require.NoError(t, err)
		assert.False(t, exists,
			"DryRun must suppress the write; the control arm proves this path writes otherwise")
	})
}

// TestApplyHooksDryRunLeavesExistingSettingsByteIdentical is the regression
// that matters most. The incident was not a file being CREATED — it was an
// existing settings.json being REWRITTEN, losing a hook nobody noticed was
// gone. Absence-of-a-new-file cannot catch that; only byte equality against
// pre-existing content can.
func TestApplyHooksDryRunLeavesExistingSettingsByteIdentical(t *testing.T) {
	const settingsPath = "/project/.claude/settings.json"
	// Content a real apply would rewrite: a hook ctxloom does not manage.
	existing := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"command":"guard","type":"command"}]}]}}`)

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.claude", 0o755))
	require.NoError(t, afero.WriteFile(fs, settingsPath, existing, 0o644))

	loader := func() (*config.Config, error) {
		return cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
			},
		}, config.Fixture{}), nil
	}

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend: "claude-code",
		FS:      fs,
		Cfg:     loaded(t, loader),
		WorkDir: "/project",
		DryRun:  true,
	})
	require.NoError(t, err)

	got, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)
	require.NotEmpty(t, got, "comparing two empty reads would be trivially identical")
	assert.Equal(t, string(existing), string(got),
		"a dry run must leave an existing settings file byte-identical")
}
