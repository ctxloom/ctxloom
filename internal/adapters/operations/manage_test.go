package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// wireClaudeHarness writes a ctxloom-managed hook set into dir's claude-code
// settings so removal/status have something to act on.
func wireClaudeHarness(t *testing.T, fs afero.Fs, dir string) {
	t.Helper()
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-start"}},
		},
	}
	deliverManagedSettings(t, "claude-code", hooks, map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}}, true, dir, fs)
}

func TestHarnessStatus_ReportsWiring(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := HarnessStatus(context.Background(), engines.Registry(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)

	assert.Equal(t, dir, res.WorkDir)

	claude := backendWiring(t, res, "claude-code")
	assert.True(t, claude.HooksPresent)
	assert.True(t, claude.StatusLine)
	assert.True(t, claude.MCPPresent)
}

// TestMaterializeRelease_EveryEngineStripsWiring is `manage uninstall`'s
// removal: a release for every registered engine at the project root takes
// out what an earlier delivery wired there.
func TestMaterializeRelease_EveryEngineStripsWiring(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Engines: EngineNames(engines.Registry()), Release: true, Force: true, Root: safefs.NewMem(fs), Target: dir,
	})
	require.NoError(t, err)
	assert.Equal(t, MaterializeReleased, res.Status, "%v", res.Errors)

	status, err := HarnessStatus(context.Background(), engines.Registry(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.False(t, backendWiring(t, status, "claude-code").HooksPresent,
		"hooks must be gone after the release")
}

func TestMaterializeRelease_SingleEngineFilter(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	cfg := &config.Config{}
	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Engines: []string{"claude-code"}, Surfaces: surfacesBut(present.Context), Release: true, Root: safefs.NewMem(fs), Target: dir})
	require.NoError(t, err)
	require.Len(t, res.Engines, 1)
	assert.Equal(t, "claude-code", res.Engines[0].Engine)
}

func TestHarnessStatus_ReportsStatuslinePreference(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	wireClaudeHarness(t, fs, dir)

	off := false
	cfg := config.NewFixture(config.Fixture{Settings: config.SettingsConfig{Statusline: &off}})
	res, err := HarnessStatus(context.Background(), engines.Registry(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.False(t, res.ManageStatusline, "status reflects the statusline opt-out")
}

// TestSetStatusline_PersistsPreference proves SetStatusline's SAVE serializes
// the preference faithfully, read back via ParseConfig (a single-document
// parse, no layering).
func TestSetStatusline_PersistsPreference(t *testing.T) {
	_, appDir := loadConfigDir(t, "schema_version: 7\n")
	mgr := testApp(t, configload.WithAppDir(appDir))

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

func TestMaterialize_HonorsStatuslineOptOut(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := t.TempDir()
	off := false
	loader := func() (*config.Config, error) {
		return cfgWithProfileHooks(t, fs, filepath.Join(tmpDir, ".ctxloom"), wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}}, config.Fixture{Settings: config.SettingsConfig{Statusline: &off}}), nil
	}

	_, err := Materialize(context.Background(), engines.Registry(), loaded(t, loader), MaterializeRequest{Engines: []string{"claude-code"}, Surfaces: surfacesBut(present.Context), Root: safefs.NewMem(fs), Target: tmpDir})
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, tmpDir+"/.claude/settings.json")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "statusLine",
		"materialize must not install a statusline when the opt-out is set")
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
