package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// U102-F06 called agent.SettingsOptions.StatusLineDisabled a claude-only
// policy flag sitting in the options struct of a package 26 packages import.
// Measured, it was worse than misplaced: agent.WithStatusLineDisabled had zero
// callers anywhere in the tree, and every production construction of
// SettingsOptions sets only FS, so the flag could never be true through that
// route. The live statusline policy rides DeliverSettings(hooks,
// manageStatusline) on the surfaces seam instead.
//
// This pins the shape the deletion leaves behind, so re-introducing a
// constructor-level knob is a visible change: the settings-writer CONSTRUCTOR
// carries no statusline policy (its writer always manages the HUD), and
// DeliverSettings is the one seam that turns it off.
func TestSettingsWriterConstructorCarriesNoStatuslinePolicy(t *testing.T) {
	w, ok := NewWriter(agent.SettingsOptions{FS: afero.NewMemMapFs()}).(*ClaudeCodeHookWriter)
	require.True(t, ok)
	assert.False(t, w.statusLineDisabled,
		"the settings-writer constructor takes no statusline input; the surfaces seam owns that policy")

	dir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: dir}, nil)
	handle, err := d.DeliverSettings(&wire.HooksConfig{}, false)
	require.NoError(t, err)
	require.NotNil(t, handle)

	data, rerr := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, rerr)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.NotContains(t, settings, "statusLine",
		"manageStatusline=false is the one seam that disables the managed HUD")
}
