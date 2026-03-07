// Context file tests verify that assembled context is persisted correctly and
// can be retrieved by the context injection hook. The hash-based naming enables
// content-addressable storage and cache invalidation when context changes.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Context File Writing Tests
// =============================================================================
// Context files are written to .scm/context/ with content-based hash filenames.
// This enables the SessionStart hook to inject the correct context.

func TestWriteContextFile(t *testing.T) {
	t.Run("writes content and returns hash", func(t *testing.T) {
		// Hash enables content-addressable lookup by the injection hook
		tmpDir := t.TempDir()
		fragments := []*Fragment{
			{Content: "First fragment"},
			{Content: "Second fragment"},
		}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)
		assert.Len(t, hash, 16) // 8 bytes = 16 hex chars

		// Verify file exists
		contextPath := filepath.Join(tmpDir, SCMContextSubdir, hash+".md")
		content, err := os.ReadFile(contextPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), "First fragment")
		assert.Contains(t, string(content), "Second fragment")
	})

	t.Run("returns empty for no content", func(t *testing.T) {
		// Empty content should not create files - avoids polluting the context dir
		tmpDir := t.TempDir()

		hash, err := WriteContextFile(tmpDir, nil)
		require.NoError(t, err)
		assert.Empty(t, hash)

		hash, err = WriteContextFile(tmpDir, []*Fragment{})
		require.NoError(t, err)
		assert.Empty(t, hash)

		// Empty content fragments also produce no file
		hash, err = WriteContextFile(tmpDir, []*Fragment{{Content: ""}, {Content: ""}})
		require.NoError(t, err)
		assert.Empty(t, hash)
	})

	t.Run("skips empty fragments", func(t *testing.T) {
		// Empty fragments are filtered out to avoid noise in context
		tmpDir := t.TempDir()
		fragments := []*Fragment{
			{Content: "Valid content"},
			{Content: ""},
			{Content: "Another valid"},
		}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)

		content, err := ReadContextFile(tmpDir, hash)
		require.NoError(t, err)
		assert.Contains(t, content, "Valid content")
		assert.Contains(t, content, "Another valid")
	})

	t.Run("creates directory if not exists", func(t *testing.T) {
		// Auto-create context directory for first-time setup
		tmpDir := t.TempDir()
		fragments := []*Fragment{{Content: "Test content"}}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)

		// Verify directory was created
		contextDir := filepath.Join(tmpDir, SCMContextSubdir)
		info, err := os.Stat(contextDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("same content produces same hash", func(t *testing.T) {
		// Content-addressable storage means same input = same hash
		// This enables caching and avoids redundant file writes
		tmpDir := t.TempDir()
		fragments := []*Fragment{{Content: "Consistent content"}}

		hash1, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		hash2, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		assert.Equal(t, hash1, hash2)
	})
}

// =============================================================================
// Context File Reading Tests
// =============================================================================
// Reading is used by the hook to retrieve context for injection.

func TestReadContextFile(t *testing.T) {
	t.Run("reads existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		fragments := []*Fragment{{Content: "Test content"}}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := ReadContextFile(tmpDir, hash)
		require.NoError(t, err)
		assert.Equal(t, "Test content", content)
	})

	t.Run("returns empty for non-existent file", func(t *testing.T) {
		// Missing file is not an error - context may have been cleaned up
		tmpDir := t.TempDir()

		content, err := ReadContextFile(tmpDir, "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, content)
	})
}

// =============================================================================
// Read-and-Delete Tests
// =============================================================================
// One-time read with cleanup prevents stale context accumulation and ensures
// context is only injected once per session.

func TestReadContextFileAndDelete(t *testing.T) {
	t.Run("reads and deletes file", func(t *testing.T) {
		// Delete after read ensures one-time injection
		tmpDir := t.TempDir()
		contextDir := filepath.Join(tmpDir, SCMContextSubdir)
		require.NoError(t, os.MkdirAll(contextDir, 0755))

		testFile := filepath.Join(contextDir, "test.md")
		require.NoError(t, os.WriteFile(testFile, []byte("Content to read"), 0644))

		t.Setenv(SCMContextFileEnv, testFile)

		content, err := ReadContextFileAndDelete(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, "Content to read", content)

		// File should be deleted
		_, err = os.Stat(testFile)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("returns empty when env var not set", func(t *testing.T) {
		// No env var means no context injection configured
		t.Setenv(SCMContextFileEnv, "")

		content, err := ReadContextFileAndDelete(".")
		require.NoError(t, err)
		assert.Empty(t, content)
	})

	t.Run("returns empty for non-existent file", func(t *testing.T) {
		// Missing file is gracefully handled - context may have been deleted
		t.Setenv(SCMContextFileEnv, "/nonexistent/path/file.md")

		content, err := ReadContextFileAndDelete(".")
		require.NoError(t, err)
		assert.Empty(t, content)
	})

	t.Run("handles relative path", func(t *testing.T) {
		// Relative paths are resolved against work directory
		tmpDir := t.TempDir()
		contextDir := filepath.Join(tmpDir, SCMContextSubdir)
		require.NoError(t, os.MkdirAll(contextDir, 0755))

		testFile := filepath.Join(contextDir, "relative.md")
		require.NoError(t, os.WriteFile(testFile, []byte("Relative content"), 0644))

		// Set relative path
		t.Setenv(SCMContextFileEnv, filepath.Join(SCMContextSubdir, "relative.md"))

		content, err := ReadContextFileAndDelete(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, "Relative content", content)
	})
}
