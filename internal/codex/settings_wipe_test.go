package codex

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// U045-F02: an unparseable config.toml degrades to an EMPTY table (load warns
// and returns map[string]any{}), which save then wrote back over the real
// file — a 0-byte config.toml, the user's codex configuration destroyed, exit
// 0 and a success path. The write must refuse instead.
func TestWriteSettings_UnparseableConfigIsNotWipedOut(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	path := w.SettingsPath("/proj")
	original := "this is [not valid toml at all\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0o644))

	err := w.WriteSettingsWithTrust(&wire.HooksConfig{}, nil, nil, "/proj", "")
	assert.Error(t, err, "writing an empty table over a real config.toml must be refused")

	data, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Equal(t, original, string(data), "the user's config.toml must survive")
}

// A genuine removal still empties the file: stripping ctxloom's own keys from
// a config that held nothing else is a legitimate empty result, not a wipe.
func TestRemoveSettings_MayLegitimatelyEmptyTheFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	path := w.SettingsPath("/proj")
	require.NoError(t, w.WriteSettingsWithTrust(&wire.HooksConfig{}, &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{"ctxloom": {Command: "ctxloom", Args: []string{"mcp"}}},
	}, nil, "/proj", ""))
	require.NoError(t, w.RemoveSettings("/proj"))

	data, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.NotContains(t, string(data), "ctxloom")
}
