package codex

import (
	"io/fs"
	"os"
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

// statErrFs fails every Stat with a fixed error — the shape of a config.toml
// under a directory the process may not traverse. afero.Exists reports
// (false, err) there, and "false" alone is indistinguishable from "absent".
type statErrFs struct {
	afero.Fs
	err error
}

func (f statErrFs) Stat(string) (os.FileInfo, error) { return nil, f.err }

// U045-F11: an UNREADABLE config.toml must not be reported as an ABSENT one.
// RemoveSettings returning nil there claims to have removed ctxloom's hooks and
// MCP servers from a file it never opened, and Status reports the engine as
// unconfigured — both are answers about a file nobody could look at.
func TestSettings_UnreadableConfigIsNotReportedAsAbsent(t *testing.T) {
	fs := statErrFs{Fs: afero.NewMemMapFs(), err: fs.ErrPermission}
	w := &CodexHookWriter{FS: fs}

	err := w.RemoveSettings("/proj")
	require.Error(t, err, "an unreadable config.toml is not a clean removal")
	assert.ErrorIs(t, err, os.ErrPermission)

	status, err := w.Status("/proj")
	require.Error(t, err, "an unreadable config.toml is not 'not configured'")
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.False(t, status.SettingsExists)
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
