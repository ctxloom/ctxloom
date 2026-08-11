package operations

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// wireClaudeHarness writes a ctxloom-managed hook set into dir's claude-code
// settings so removal/status have something to act on.
func wireClaudeHarness(t *testing.T, fs afero.Fs, dir string) {
	t.Helper()
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		},
	}
	deliverManagedSettings(t, "claude-code", hooks, nil, nil, true, dir, fs)
}

func TestHarnessStatus_ReportsWiringAndAutoRegister(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := HarnessStatus(context.Background(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)

	assert.Equal(t, dir, res.WorkDir)
	assert.True(t, res.AutoRegisterMCP, "auto-register defaults on")

	claude := backendWiring(t, res, "claude-code")
	assert.True(t, claude.HooksPresent)
	assert.True(t, claude.StatusLine)
	assert.True(t, claude.MCPPresent)

	kiro := backendWiring(t, res, "kiro")
	assert.False(t, kiro.SettingsExists, "untouched backend reports nothing wired")
}

func TestRemoveHooks_StripsWiring(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := RemoveHooks(context.Background(), cfg, RemoveHooksRequest{Backend: "all", FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "removed", res.Status)
	assert.Contains(t, res.Backends, "claude-code")

	status, err := HarnessStatus(context.Background(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.False(t, backendWiring(t, status, "claude-code").HooksPresent,
		"hooks must be gone after RemoveHooks")
}

func TestRemoveHooks_SingleBackendFilter(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := RemoveHooks(context.Background(), cfg, RemoveHooksRequest{Backend: "claude-code", FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-code"}, res.Backends)
}

func TestHarnessStatus_ReportsStatuslinePreference(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	off := false
	cfg := config.NewFixture(config.Fixture{Settings: config.SettingsConfig{Statusline: &off}})
	res, err := HarnessStatus(context.Background(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.False(t, res.ManageStatusline, "status reflects the statusline opt-out")
}

// TestSetStatusline_PersistsPreference proves SetStatusline's SAVE serializes
// the preference faithfully. Read back via ParseConfig (a single-document
// parse, no layering) rather than a full config.Load: config.statusline is
// ScopeMachine (internal/config/layerscope) — whether ctxloom may own THIS
// terminal is a per-machine fact, so a committed PROJECT file (every clone's
// copy, which is what SetStatusline writes to — there is no separate
// machine-scoped project file yet) no longer has it take effect on a real
// Load. What this test still pins is that the write itself is faithful.
func TestSetStatusline_PersistsPreference(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := config.NewManager(config.WithAppDir(appDir))

	res, err := SetStatusline(context.Background(), mgr, SetStatuslineRequest{Enabled: false})
	require.NoError(t, err)
	assert.False(t, res.Statusline)

	data, err := os.ReadFile(paths.ConfigPath(appDir))
	require.NoError(t, err)
	reloaded, err := config.ParseConfig(data)
	require.NoError(t, err)
	require.NotNil(t, reloaded.GetSettings().Statusline)
	assert.False(t, *reloaded.GetSettings().Statusline, "the toggle records the preference in config")
}

func TestApplyHooks_HonorsStatuslineOptOut(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := t.TempDir()
	off := false
	loader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Settings: config.SettingsConfig{Statusline: &off},
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
				},
			},
		}), nil
	}

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: loader,
		WorkDir:      tmpDir,
	})
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, tmpDir+"/.claude/settings.json")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "statusLine",
		"apply-hooks must not install a statusline when the opt-out is set")
}

// backendWiring returns the named backend's wiring from a status result.
func backendWiring(t *testing.T, res *HarnessStatusResult, name string) BackendWiring {
	t.Helper()
	for _, b := range res.Backends {
		if b.Backend == name {
			return b
		}
	}
	t.Fatalf("backend %q not found in status result", name)
	return BackendWiring{}
}
