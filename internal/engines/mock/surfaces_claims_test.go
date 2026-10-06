package mock

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestContextFile_ClaimsASectionAfterTheUsersText: the mock's context file
// is one a human may also write, so the context is a SECTION claimed after
// the file's own text — written by the static writer, never by the approach.
func TestContextFile_ClaimsASectionAfterTheUsersText(t *testing.T) {
	fs := afero.NewMemMapFs()
	a, ok := New().Root().Surfaces()[present.Context].(engine.ContextApproach)
	require.True(t, ok)
	d, err := a.DeliverContext(present.ProjectOnHost("/p"), present.RootProjectRoot, engine.ContextInputs{Text: []byte("rules")}, fs)
	require.NoError(t, err)
	path := filepath.Join("/p", ContextFileName)
	require.Equal(t, path, d.Presented.HostPath)
	require.Equal(t, map[string][]present.Claim{path: {{Pointer: present.AppendedSection, Value: []byte("rules")}}}, d.Claims)
	exists, err := afero.Exists(fs, path)
	require.NoError(t, err)
	require.False(t, exists, "the approach writes nothing")
}

// TestSettingsFile_RecordsTheShellTimeout: the mock's settings file records
// every settings input it is handed, the engine-neutral shell timeout in
// milliseconds among them, so a suite can see it reach an engine that is not
// claude.
func TestSettingsFile_RecordsTheShellTimeout(t *testing.T) {
	fs := afero.NewMemMapFs()
	a, ok := New().Root().Surfaces()[present.Settings].(engine.SettingsApproach)
	require.True(t, ok)
	st := engine.ShellTimeout{Default: 10 * time.Minute, Max: time.Hour}
	_, err := a.DeliverSettings(present.ProjectOnHost("/p"), present.RootProjectRoot, engine.SettingsInputs{ShellTimeout: st}, fs)
	require.NoError(t, err)
	b, err := afero.ReadFile(fs, filepath.Join("/p", settingsRel))
	require.NoError(t, err)
	var doc struct {
		ShellTimeout struct{ DefaultMs, MaxMs int64 } `json:"shellTimeout"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	require.Equal(t, int64(600000), doc.ShellTimeout.DefaultMs)
	require.Equal(t, int64(3600000), doc.ShellTimeout.MaxMs)
}
