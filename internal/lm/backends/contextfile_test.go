// Context file tests verify that assembled context is persisted correctly and
// can be retrieved by the context injection hook. The hash-based naming enables
// content-addressable storage and cache invalidation when context changes.
package backends

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Context File Writing Tests
// =============================================================================
// Context files are written to .ctxloom/context/ with content-based hash filenames.
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

	t.Run("deduplicates identical content from multiple fragments", func(t *testing.T) {
		// When the same fragment content exists in multiple bundles (e.g., due to
		// duplicate bundle installations), it should only appear once in the output.
		// This is critical for avoiding wasted tokens from duplicate context.
		tmpDir := t.TempDir()
		duplicateContent := "# Go Testing\n\nThis is testing content."
		fragments := []*Fragment{
			{Name: "testing", Content: duplicateContent},
			{Name: "other/testing", Content: duplicateContent}, // Same content, different name
			{Name: "unique", Content: "Unique content here"},
		}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := ReadContextFile(tmpDir, hash)
		require.NoError(t, err)

		// Count occurrences - duplicate content should only appear once
		count := countOccurrences(content, "# Go Testing")
		assert.Equal(t, 1, count, "duplicate content should only appear once")

		// Unique content should still be present
		assert.Contains(t, content, "Unique content here")
	})

	t.Run("preserves fragments with different content", func(t *testing.T) {
		// Different content should all be preserved even if names are similar
		tmpDir := t.TempDir()
		fragments := []*Fragment{
			{Name: "frag1", Content: "Content A"},
			{Name: "frag2", Content: "Content B"},
			{Name: "frag3", Content: "Content C"},
		}

		hash, err := WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := ReadContextFile(tmpDir, hash)
		require.NoError(t, err)

		assert.Contains(t, content, "Content A")
		assert.Contains(t, content, "Content B")
		assert.Contains(t, content, "Content C")
	})
}

// countOccurrences counts non-overlapping occurrences of substr in s.
func countOccurrences(s, substr string) int {
	count := 0
	for {
		idx := indexString(s, substr)
		if idx == -1 {
			break
		}
		count++
		s = s[idx+len(substr):]
	}
	return count
}

func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// =============================================================================
// Size Warning Tests
// =============================================================================
// Large context files trigger warnings to help users avoid LLM degradation.

func TestWriteContextFile_SizeWarnings(t *testing.T) {
	t.Run("warns when content exceeds max size", func(t *testing.T) {
		tmpDir := t.TempDir()
		var stderr bytes.Buffer

		// Create content larger than MaxRecommendedContextSize
		largeContent := strings.Repeat("x", MaxRecommendedContextSize+1024)
		fragments := []*Fragment{{Content: largeContent}}

		_, err := WriteContextFile(tmpDir, fragments, WithContextStderr(&stderr))
		require.NoError(t, err)

		warnings := stderr.String()
		assert.Contains(t, warnings, "ctxloom: warning: assembled context is")
		assert.Contains(t, warnings, WarnContextEffectiveness)
	})

	t.Run("no warning when content is under max size", func(t *testing.T) {
		tmpDir := t.TempDir()
		var stderr bytes.Buffer

		// Create content under MaxRecommendedContextSize
		smallContent := strings.Repeat("x", MaxRecommendedContextSize-1024)
		fragments := []*Fragment{{Content: smallContent}}

		_, err := WriteContextFile(tmpDir, fragments, WithContextStderr(&stderr))
		require.NoError(t, err)

		assert.Empty(t, stderr.String())
	})

	t.Run("no warning at exactly max size boundary", func(t *testing.T) {
		tmpDir := t.TempDir()
		var stderr bytes.Buffer

		// Create content exactly at MaxRecommendedContextSize
		boundaryContent := strings.Repeat("x", MaxRecommendedContextSize)
		fragments := []*Fragment{{Content: boundaryContent}}

		_, err := WriteContextFile(tmpDir, fragments, WithContextStderr(&stderr))
		require.NoError(t, err)

		assert.Empty(t, stderr.String())
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
