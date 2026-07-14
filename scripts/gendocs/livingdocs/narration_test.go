package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNarration_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j1_setup.doc.md")
	writeFile(t, path, `Some preamble text not inside any marker (ignored).

<!-- doc:intro -->
This is the intro.
<!-- /doc:intro -->

<!-- doc:scenario: First scenario -->
Prose about the first scenario.
<!-- /doc:scenario -->

<!-- doc:outro -->
This is the outro.
<!-- /doc:outro -->
`)

	narr, err := LoadNarration(path)
	require.NoError(t, err)

	assert.Equal(t, "This is the intro.", narr.Intro)
	assert.Equal(t, "This is the outro.", narr.Outro)
	require.Contains(t, narr.Scenarios, "First scenario")
	assert.Equal(t, "Prose about the first scenario.", narr.Scenarios["First scenario"])
}

func TestLoadNarration_MissingFileIsNotAnError(t *testing.T) {
	narr, err := LoadNarration("/no/such/file.doc.md")
	require.NoError(t, err)
	assert.Empty(t, narr.Intro)
	assert.Empty(t, narr.Outro)
	assert.Empty(t, narr.Scenarios)
}

func TestLoadNarration_NoMarkersIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.doc.md")
	writeFile(t, path, "Just some prose, no markers at all.\n")

	narr, err := LoadNarration(path)
	require.NoError(t, err)
	assert.Empty(t, narr.Intro)
	assert.Empty(t, narr.Outro)
	assert.Empty(t, narr.Scenarios)
}

func TestLoadNarration_MultipleScenarios(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.doc.md")
	writeFile(t, path, `<!-- doc:scenario: One -->
First.
<!-- /doc:scenario -->
<!-- doc:scenario: Two -->
Second.
<!-- /doc:scenario -->
`)

	narr, err := LoadNarration(path)
	require.NoError(t, err)
	require.Len(t, narr.Scenarios, 2)
	assert.Equal(t, "First.", narr.Scenarios["One"])
	assert.Equal(t, "Second.", narr.Scenarios["Two"])
}
