package backends

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// statusLineCommand returns the configured statusLine command for dir's
// claude-code settings, or "" when none is set.
func statusLineCommand(t *testing.T, fs afero.Fs, dir string) string {
	t.Helper()
	data, err := afero.ReadFile(fs, dir+"/.claude/settings.json")
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return ""
	}
	cmd, _ := sl["command"].(string)
	return cmd
}

func TestManagedSettings_StatusLineEnabled_InstallsHud(t *testing.T) {
	fs := afero.NewMemMapFs()
	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), nil, true, "/p", fs)
	assert.Equal(t, "ctxloom hook hud", statusLineCommand(t, fs, "/p"))
}

func TestManagedSettings_StatusLineDisabled_DoesNotInstall(t *testing.T) {
	fs := afero.NewMemMapFs()
	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), nil, false, "/p", fs)
	assert.Empty(t, statusLineCommand(t, fs, "/p"), "no HUD statusline when disabled")
}

func TestManagedSettings_StatusLineDisabled_ClearsPreviouslyManaged(t *testing.T) {
	fs := afero.NewMemMapFs()
	// First install with the HUD, then re-apply with the opt-out.
	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), nil, true, "/p", fs)
	require.Equal(t, "ctxloom hook hud", statusLineCommand(t, fs, "/p"))

	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), nil, false, "/p", fs)
	assert.Empty(t, statusLineCommand(t, fs, "/p"), "a ctxloom-managed statusline is cleared on opt-out")
}

func TestManagedSettings_StatusLineDisabled_PreservesUserStatusline(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/p/.claude", 0755))
	user := `{"statusLine":{"type":"command","command":"my-own-statusline"}}`
	require.NoError(t, afero.WriteFile(fs, "/p/.claude/settings.json", []byte(user), 0644))

	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), nil, false, "/p", fs)

	assert.Equal(t, "my-own-statusline", statusLineCommand(t, fs, "/p"),
		"a user's own statusline is never touched")
}

// TestShouldManageStatusline_Default confirms the config default keeps the HUD on.
func TestShouldManageStatusline_Default(t *testing.T) {
	var s config.SettingsConfig
	assert.True(t, s.ShouldManageStatusline(), "statusline defaults to managed")
	off := false
	s.Statusline = &off
	assert.False(t, s.ShouldManageStatusline())
}
