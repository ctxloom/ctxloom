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

// An unparseable config.toml used to degrade to an EMPTY table (load warns
// and returns map[string]any{}), which save then wrote back over the real
// file — a 0-byte config.toml, the user's codex configuration destroyed, exit
// 0 and a success path. The write must refuse instead.
func TestWriteSettings_UnparseableConfigIsNotWipedOut(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	path := w.settingsPathIn("/proj")
	original := "this is [not valid toml at all\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0o644))

	err := w.writeSettingsIn(&wire.HooksConfig{}, nil, "/proj", "")
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

// An UNREADABLE config.toml must not be reported as an ABSENT one. Returning
// nil there claims to have removed ctxloom's hooks and MCP servers from a file
// nobody could open.
//
// This is asserted on the RESOLVED-home revert (removeSettingsIn, the one a
// delivery handle's Cleanup calls), which is the only path that reads a
// config.toml at all. Its harpless sibling, RemoveSettings, cannot reach this
// case: it opens no file, because there is none to open (declared_absence.go),
// and its own behaviour is pinned in TestRemoveSettings_SaysNothingToRemove.
func TestSettings_UnreadableConfigIsNotReportedAsAbsent(t *testing.T) {
	fs := statErrFs{Fs: afero.NewMemMapFs(), err: fs.ErrPermission}
	w := &CodexHookWriter{FS: fs}

	err := w.removeSettingsIn("/proj")
	require.Error(t, err, "an unreadable config.toml is not a clean removal")
	assert.ErrorIs(t, err, os.ErrPermission)
}

// A genuine removal still empties the file: stripping ctxloom's own keys from
// a config that held nothing else is a legitimate empty result, not a wipe.
func TestRemoveSettings_MayLegitimatelyEmptyTheFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	path := w.settingsPathIn("/proj")
	require.NoError(t, w.writeSettingsIn(&wire.HooksConfig{},
		map[string]wire.MCPServer{"ctxloom": {Command: "ctxloom", Args: []string{"mcp"}}}, "/proj", ""))
	require.NoError(t, w.removeSettingsIn("/proj"))

	data, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.NotContains(t, string(data), "ctxloom")
}
