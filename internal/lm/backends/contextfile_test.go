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

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Context File Writing Tests
// =============================================================================
// Context files are written to .ctxloom/ephemeral/context/ with content-based hash filenames.
// This enables the SessionStart hook to inject the correct context.

func TestWriteContextFile(t *testing.T) {
	t.Run("writes content and returns hash", func(t *testing.T) {
		// Hash enables content-addressable lookup by the injection hook
		tmpDir := t.TempDir()
		fragments := []*agent.Fragment{
			{Content: "First fragment"},
			{Content: "Second fragment"},
		}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)
		assert.Len(t, hash, 16) // 8 bytes = 16 hex chars

		// Verify file exists
		contextPath := filepath.Join(tmpDir, agent.SCMContextSubdir, hash+".md")
		content, err := os.ReadFile(contextPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), "First fragment")
		assert.Contains(t, string(content), "Second fragment")
	})

	t.Run("returns empty for no content", func(t *testing.T) {
		// Empty content should not create files - avoids polluting the context dir
		tmpDir := t.TempDir()

		hash, err := agent.WriteContextFile(tmpDir, nil)
		require.NoError(t, err)
		assert.Empty(t, hash)

		hash, err = agent.WriteContextFile(tmpDir, []*agent.Fragment{})
		require.NoError(t, err)
		assert.Empty(t, hash)

	})

	// No fragments at all is legitimately nothing to do; fragments that
	// EXIST but assemble to nothing is a delivery failure wearing the same
	// clothes. The two must not be reported identically.
	t.Run("fragments that assemble to nothing are an error", func(t *testing.T) {
		tmpDir := t.TempDir()

		hash, err := agent.WriteContextFile(tmpDir, []*agent.Fragment{{Content: ""}, {Content: ""}})
		require.Error(t, err, "2 fragments produced zero bytes of context — that is a failure, not an empty config")
		assert.ErrorIs(t, err, agent.ErrNoContext)
		assert.Empty(t, hash)
	})

	t.Run("skips empty fragments", func(t *testing.T) {
		// Empty fragments are filtered out to avoid noise in context
		tmpDir := t.TempDir()
		fragments := []*agent.Fragment{
			{Content: "Valid content"},
			{Content: ""},
			{Content: "Another valid"},
		}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)

		content, err := agent.ReadContextFile(tmpDir, hash)
		require.NoError(t, err)
		assert.Contains(t, content, "Valid content")
		assert.Contains(t, content, "Another valid")
	})

	t.Run("creates directory if not exists", func(t *testing.T) {
		// Auto-create context directory for first-time setup
		tmpDir := t.TempDir()
		fragments := []*agent.Fragment{{Content: "Test content"}}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)

		// Verify directory was created
		contextDir := filepath.Join(tmpDir, agent.SCMContextSubdir)
		info, err := os.Stat(contextDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("same content produces same hash", func(t *testing.T) {
		// Content-addressable storage means same input = same hash
		// This enables caching and avoids redundant file writes
		tmpDir := t.TempDir()
		fragments := []*agent.Fragment{{Content: "Consistent content"}}

		hash1, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		hash2, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		assert.Equal(t, hash1, hash2)
	})

	t.Run("deduplicates one fragment that reached the list twice", func(t *testing.T) {
		// A fragment that arrives twice under its own identity is one piece of
		// content, and writing it twice is wasted tokens for nothing.
		tmpDir := t.TempDir()
		duplicateContent := "# Go Testing\n\nThis is testing content."
		fragments := []*agent.Fragment{
			{Name: "other/testing", Content: duplicateContent},
			{Name: "other/testing", Content: duplicateContent}, // the SAME item, again
			{Name: "unique", Content: "Unique content here"},
		}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := agent.ReadContextFile(tmpDir, hash)
		require.NoError(t, err)

		count := countOccurrences(content, "# Go Testing")
		assert.Equal(t, 1, count, "one item delivered twice must be written once")

		// Unique content should still be present
		assert.Contains(t, content, "Unique content here")
	})

	t.Run("keeps two different fragments whose content happens to match", func(t *testing.T) {
		// This used to collapse, because dedup keyed on the content hash ALONE.
		// Two separately named fragments are two authored items; dropping one of
		// them delivers one publisher's content in place of another's, which is
		// data loss wearing deduplication's clothes. The identity is (Name,
		// content) — the same rule operations.contextIngest applies at the ingest
		// layer, expressed with the identity this layer carries.
		tmpDir := t.TempDir()
		shared := "# Go Testing\n\nThis is testing content."
		fragments := []*agent.Fragment{
			{Name: "testing", Content: shared},
			{Name: "other/testing", Content: shared}, // same content, DIFFERENT item
			{Name: "unique", Content: "Unique content here"},
		}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := agent.ReadContextFile(tmpDir, hash)
		require.NoError(t, err)

		assert.Equal(t, 2, countOccurrences(content, "# Go Testing"),
			"two different fragments that merely say the same thing must both be written")
		assert.Contains(t, content, "Unique content here")
	})

	t.Run("preserves fragments with different content", func(t *testing.T) {
		// Different content should all be preserved even if names are similar
		tmpDir := t.TempDir()
		fragments := []*agent.Fragment{
			{Name: "frag1", Content: "Content A"},
			{Name: "frag2", Content: "Content B"},
			{Name: "frag3", Content: "Content C"},
		}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := agent.ReadContextFile(tmpDir, hash)
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
		largeContent := strings.Repeat("x", agent.MaxRecommendedContextSize+1024)
		fragments := []*agent.Fragment{{Content: largeContent}}

		_, err := agent.WriteContextFile(tmpDir, fragments, agent.WithContextStderr(&stderr))
		require.NoError(t, err)

		warnings := stderr.String()
		assert.Contains(t, warnings, "ctxloom: warning: assembled context is")
		assert.Contains(t, warnings, agent.WarnContextEffectiveness)
	})

	t.Run("no warning when content is under max size", func(t *testing.T) {
		tmpDir := t.TempDir()
		var stderr bytes.Buffer

		// Create content under MaxRecommendedContextSize
		smallContent := strings.Repeat("x", agent.MaxRecommendedContextSize-1024)
		fragments := []*agent.Fragment{{Content: smallContent}}

		_, err := agent.WriteContextFile(tmpDir, fragments, agent.WithContextStderr(&stderr))
		require.NoError(t, err)

		assert.Empty(t, stderr.String())
	})

	t.Run("no warning at exactly max size boundary", func(t *testing.T) {
		tmpDir := t.TempDir()
		var stderr bytes.Buffer

		// Create content exactly at MaxRecommendedContextSize
		boundaryContent := strings.Repeat("x", agent.MaxRecommendedContextSize)
		fragments := []*agent.Fragment{{Content: boundaryContent}}

		_, err := agent.WriteContextFile(tmpDir, fragments, agent.WithContextStderr(&stderr))
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
		fragments := []*agent.Fragment{{Content: "Test content"}}

		hash, err := agent.WriteContextFile(tmpDir, fragments)
		require.NoError(t, err)

		content, err := agent.ReadContextFile(tmpDir, hash)
		require.NoError(t, err)
		assert.Equal(t, "Test content", content)
	})

	// A reaped or never-written context file used to read as ("", nil) —
	// indistinguishable from "no context configured". Callers are only ever
	// here because a hash exists, so the file's absence is a real fact they
	// must be able to report.
	t.Run("missing file is reported, not silently empty", func(t *testing.T) {
		tmpDir := t.TempDir()

		content, err := agent.ReadContextFile(tmpDir, "nonexistent")
		require.Error(t, err, "a missing context file must be distinguishable from empty context")
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.Empty(t, content)
	})
}

// =============================================================================
// Read-and-Delete Tests
// =============================================================================
