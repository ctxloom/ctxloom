package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoader_Load_ProfileHooksExt pins the profile file as the on-disk surface
// of the engine-namespaced hook map: `hooks.ext.<engine>.<native event>`.
func TestLoader_Load_ProfileHooksExt(t *testing.T) {
	dir := projectProfilesDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(
		"hooks:\n  ext:\n    claude-code:\n      PreToolUse:\n        - command: x\n          type: command\n",
	), 0o644))

	p, err := osLoader(t, dir).Load("p")
	require.NoError(t, err)
	require.Len(t, p.Hooks.Ext["claude-code"]["PreToolUse"], 1)
	assert.Equal(t, "x", p.Hooks.Ext["claude-code"]["PreToolUse"][0].Command)
}

// TestDecode_ProfileHooksUnknownKeyFails pins the loud failure at the surface
// a user actually edits: a profile whose hooks block carries a key the type
// does not model does not decode. (The bundle's content decoder wraps it with
// the file.)
func TestDecode_ProfileHooksUnknownKeyFails(t *testing.T) {
	p, err := Decode([]byte(
		"hooks:\n  plugins:\n    claude-code:\n      PreToolUse:\n        - command: x\n          type: command\n",
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins")
	assert.Nil(t, p, "a refused profile must not load half-decoded, with its engine hooks silently gone")
}
