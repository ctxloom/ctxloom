package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNarration_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j000200_setup.doc.md")
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

// TestGeneratePage_UnmatchedNarrationIsSilentlyDropped characterizes the
// defect before the fix: prose keyed to a scenario name that does not exist is
// parsed into Narration.Scenarios, never looked up by renderScenario, and
// discarded with no signal — GeneratePage returns a page and a nil error, and
// the author's hand-written paragraph is simply not in it.
func TestGeneratePage_UnmatchedNarrationIsSilentlyDropped(t *testing.T) {
	feat := Feature{Path: "f.feature", Name: "F", Scenarios: []Scenario{{Name: "Real", Body: "Scenario: Real"}}}
	narr := Narration{Scenarios: map[string]string{
		"Real":        "kept prose",
		"Typoed Name": "ORPHANED PROSE",
	}}
	page, err := GeneratePage(feat, narr, nil, "")
	if err != nil {
		t.Fatalf("GeneratePage: %v", err)
	}
	if !strings.Contains(page, "kept prose") {
		t.Errorf("matched narration missing from the page")
	}
	if strings.Contains(page, "ORPHANED PROSE") {
		t.Errorf("unmatched narration unexpectedly rendered")
	}
}

// TestOrphanNarrations names exactly the markers whose scenario does not
// exist, so the discard above stops being silent.
func TestOrphanNarrations(t *testing.T) {
	feat := Feature{Scenarios: []Scenario{{Name: "Real"}, {Name: "Also Real"}}}

	if got := OrphanNarrations(feat, Narration{Scenarios: map[string]string{"Real": "a", "Also Real": "b"}}); len(got) != 0 {
		t.Errorf("got %v, want no orphans when every marker matches", got)
	}

	got := OrphanNarrations(feat, Narration{Scenarios: map[string]string{
		"Real":    "a",
		"Zebra":   "b",
		"Aardvak": "c",
	}})
	want := []string{"Aardvak", "Zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted, so the warning is stable)", got, want)
		}
	}

	// A marker with EMPTY prose is still an orphan: the author wrote the
	// marker, so the mismatch is worth naming either way.
	if got := OrphanNarrations(feat, Narration{Scenarios: map[string]string{"Ghost": ""}}); len(got) != 1 || got[0] != "Ghost" {
		t.Errorf("got %v, want [Ghost]", got)
	}
}
