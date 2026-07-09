package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChunkContext_EmptyAndSmall covers the fast paths: empty content yields no
// chunks, and content already within the cap is returned whole as one chunk.
func TestChunkContext_EmptyAndSmall(t *testing.T) {
	assert.Nil(t, ChunkContext(""), "empty content must yield no chunks")

	small := "# Tiny\nshort body"
	got := ChunkContext(small)
	require.Len(t, got, 1, "content under the cap must be a single chunk")
	assert.Equal(t, small, got[0])
}

// TestChunkContext_SplitsOnSections verifies large content is split on section
// boundaries, every chunk stays within the cap, and the chunks reassemble to
// the original (no content lost or reordered).
func TestChunkContext_SplitsOnSections(t *testing.T) {
	// Six ~3KB sections joined the way WriteContextFile joins fragments. Two
	// fit per chunk (~6KB+sep), three would exceed the cap.
	var sections []string
	for i := range 6 {
		sections = append(sections, "# Section "+string(rune('A'+i))+"\n"+strings.Repeat("x", 3000))
	}
	content := strings.Join(sections, contextSectionSep)

	chunks := ChunkContext(content)
	require.Greater(t, len(chunks), 1, "multi-section oversized content must split")

	for i, c := range chunks {
		assert.LessOrEqualf(t, len(c), ContextChunkMaxChars,
			"chunk %d (%d chars) exceeds the cap", i, len(c))
	}
	assert.Equal(t, content, strings.Join(chunks, contextSectionSep),
		"chunks must reassemble to the original content, in order")
}

// TestChunkContext_OversizedSectionLineSplit covers the fallback: a single
// section larger than the cap is split on line boundaries, never mid-line.
func TestChunkContext_OversizedSectionLineSplit(t *testing.T) {
	// One section well over the cap, composed of many short lines.
	section := "# Big\n" + strings.Repeat("a line of text here\n", 800) // ~16KB
	require.Greater(t, len(section), ContextChunkMaxChars)

	chunks := ChunkContext(section)
	require.Greater(t, len(chunks), 1, "oversized single section must line-split")

	for i, c := range chunks {
		assert.LessOrEqualf(t, len(c), ContextChunkMaxChars,
			"line-split chunk %d (%d chars) exceeds the cap", i, len(c))
		// No chunk should start or end by splitting inside a line: every line
		// in the source is intact, so rejoining reproduces the section.
	}
	assert.Equal(t, section, strings.Join(chunks, "\n"),
		"line-split chunks must rejoin (with newline) to the original section")
}

// TestChunkContext_LongLineEmittedWhole pins the deliberate trade-off: a single
// line longer than the cap is emitted whole rather than cut mid-line.
func TestChunkContext_LongLineEmittedWhole(t *testing.T) {
	long := "# X\n" + strings.Repeat("z", ContextChunkMaxChars+500) // one giant line
	chunks := ChunkContext(long)
	require.NotEmpty(t, chunks)
	joined := strings.Join(chunks, "\n")
	assert.Equal(t, long, joined, "content must be preserved even when a line exceeds the cap")
}
