package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoader_Load_ProfileHooksExt pins the profile file as the on-disk surface
// of the engine-namespaced hook map: `hooks.ext.<engine>.<native event>`.
func TestLoader_Load_ProfileHooksExt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(
		"hooks:\n  ext:\n    claude-code:\n      PreToolUse:\n        - command: x\n          type: command\n",
	), 0o644))

	p, err := NewLoader([]string{dir}).Load("p")
	require.NoError(t, err)
	require.Len(t, p.Hooks.Ext["claude-code"]["PreToolUse"], 1)
	assert.Equal(t, "x", p.Hooks.Ext["claude-code"]["PreToolUse"][0].Command)
}

// TestLoader_Load_ProfileHooksRetiredPluginsKeyFailsNamingFile pins the loud
// failure at the surface a user actually edits: a profile file still spelling
// `hooks.plugins` does not load, the error is the wire sentinel, and it names
// the FILE — a refusal that says which document to fix is the whole remedy.
func TestLoader_Load_ProfileHooksRetiredPluginsKeyFailsNamingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"hooks:\n  plugins:\n    claude-code:\n      PreToolUse:\n        - command: x\n          type: command\n",
	), 0o644))

	p, err := NewLoader([]string{dir}).Load("p")
	require.ErrorIs(t, err, wire.ErrRetiredHooksExtKey)
	assert.Contains(t, err.Error(), path, "the refusal names the file carrying the retired key")
	assert.Contains(t, err.Error(), "'ext:'", "the refusal names the current spelling")
	assert.Nil(t, p, "a refused profile must not load half-decoded, with its engine hooks silently gone")
}
