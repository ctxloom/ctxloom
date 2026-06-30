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
}

func TestGetFS(t *testing.T) {
	memFs := afero.NewMemMapFs()
	assert.Equal(t, memFs, GetFS(memFs), "returns the provided fs")
	assert.NotNil(t, GetFS(nil), "falls back to the OS fs when nil")
}
