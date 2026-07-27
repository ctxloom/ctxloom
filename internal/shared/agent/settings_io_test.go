package agent

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the shared settings-writer helpers the per-agent writer modules
// (claude/antigravity/codex) call directly. They moved here from the host backends
// package along with the helpers themselves.

// TestResolveMCPCommand pins dire-five's fix at its narrowest seam: an empty
// override (every cell but an isolated container) returns EXACTLY
// CtxloomCommand()'s value — the host self-exec-absolute invariant is
// byte-for-byte unchanged — while a non-empty override (populated ONLY on the
// container axis, see isolation.Container.MCPCommandOverride) wins outright.
func TestResolveMCPCommand(t *testing.T) {
	assert.Equal(t, CtxloomCommand(), ResolveMCPCommand(""), "empty override must not perturb the host self-exec-absolute default")
	assert.Equal(t, "/usr/local/bin/ctxloom", ResolveMCPCommand("/usr/local/bin/ctxloom"), "a non-empty override wins outright")
}

func TestComputeHookHash(t *testing.T) {
	h1 := wire.Hook{Command: "./test.sh", Matcher: "Bash"}
	h2 := wire.Hook{Command: "./test.sh", Matcher: "Bash"}
	h3 := wire.Hook{Command: "./other.sh", Matcher: "Bash"}

	assert.Equal(t, ComputeHookHash(h1), ComputeHookHash(h2), "identical hooks → identical hash")
	assert.NotEqual(t, ComputeHookHash(h1), ComputeHookHash(h3), "different hooks → different hash")
	assert.Len(t, ComputeHookHash(h1), 16, "hash is 16 hex chars")
}

func TestAtomicWriteFile(t *testing.T) {
	t.Run("writes new file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		data := []byte(`{"key": "value"}`)

		require.NoError(t, AtomicWriteFile(fs, path, data, "test file"))
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, data, contents)
	})

	t.Run("creates backup of existing file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		original := []byte(`{"original": true}`)
		updated := []byte(`{"updated": true}`)
		require.NoError(t, afero.WriteFile(fs, path, original, 0644))

		require.NoError(t, AtomicWriteFile(fs, path, updated, "test file"))

		backup, err := afero.ReadFile(fs, path+".ctxloom.bak")
		require.NoError(t, err)
		assert.Equal(t, original, backup, "backup holds the prior content")
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, updated, contents)
	})

	t.Run("cleans up temp file on success", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{}`), "test file"))
		exists, _ := afero.Exists(fs, path+".ctxloom.tmp")
		assert.False(t, exists, "temp file is cleaned up")
	})

	t.Run("new file defaults to owner-only mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{}`), "test file"))
		info, err := fs.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "new settings files are not world-readable")
	})

	t.Run("preserves a tightened existing mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, afero.WriteFile(fs, path, []byte(`{"original": true}`), 0600))

		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{"updated": true}`), "test file"))

		info, err := fs.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "rewrite must not widen a tightened mode")
		bInfo, err := fs.Stat(path + ".ctxloom.bak")
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), bInfo.Mode().Perm(), "backup mirrors the restrictive source mode")
	})

	// U102-F08: AtomicWriteFile had no len(data)==0 guard, so a caller that
	// accidentally assembled zero bytes (an upstream bug, not an intentional
	// removal — RemoveSettings/dropManaged callers go through fs.Remove, never
	// through this path with empty data) silently truncated a live settings
	// file to zero bytes and reported success. Refuse instead — the same
	// "refuse to overwrite, never self-heal" posture corrupt-config handling
	// already uses elsewhere.
	t.Run("refuses to truncate an existing file to zero bytes", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		original := []byte(`{"real": "settings"}`)
		require.NoError(t, afero.WriteFile(fs, path, original, 0600))

		err := AtomicWriteFile(fs, path, []byte{}, "test file")
		require.Error(t, err, "zero-length data must not silently win a write over a live file")

		contents, readErr := afero.ReadFile(fs, path)
		require.NoError(t, readErr)
		assert.Equal(t, original, contents, "the original file must survive the refused write untouched")
	})
}

func TestGetFS(t *testing.T) {
	memFs := afero.NewMemMapFs()
	assert.Equal(t, memFs, GetFS(memFs), "returns the provided fs")
	assert.NotNil(t, GetFS(nil), "falls back to the OS fs when nil")
}
