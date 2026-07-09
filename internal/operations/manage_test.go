package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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

	antigravity := backendWiring(t, res, "antigravity")
	assert.False(t, antigravity.SettingsExists, "untouched backend reports nothing wired")
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
	cfg := &config.Config{Settings: config.SettingsConfig{Statusline: &off}}
	res, err := HarnessStatus(context.Background(), cfg, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	assert.False(t, res.ManageStatusline, "status reflects the statusline opt-out")
}

func TestSetStatusline_PersistsPreference(t *testing.T) {
	cfg := &config.Config{}
	res, err := SetStatusline(context.Background(), cfg, SetStatuslineRequest{
		Enabled:    false,
		TestConfig: cfg,
	})
	require.NoError(t, err)
	assert.False(t, res.Statusline)
	require.NotNil(t, cfg.Settings.Statusline)
	assert.False(t, *cfg.Settings.Statusline, "the toggle records the preference in config")
}

func TestApplyHooks_HonorsStatuslineOptOut(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := t.TempDir()
	off := false
	loader := func() (*config.Config, error) {
		return &config.Config{
			Settings: config.SettingsConfig{Statusline: &off},
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
				},
			},
		}, nil
	}

	_, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ExecPath:     "/usr/bin/ctxloom",
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
