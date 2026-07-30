package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/doccapture"
)

func TestLoadCaptures_GroupsByScenarioName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a-1.json"), `{"scenario":"Outline case","steps":[{"text":"Given a","status":"passed"}]}`)
	writeFile(t, filepath.Join(dir, "a-2.json"), `{"scenario":"Outline case","steps":[{"text":"Given b","status":"passed"}]}`)
	writeFile(t, filepath.Join(dir, "b-1.json"), `{"scenario":"Other case","steps":[{"text":"Given c","status":"passed"}]}`)

	captures, err := LoadCaptures(dir)
	require.NoError(t, err)

	require.Len(t, captures["Outline case"], 2)
	require.Len(t, captures["Other case"], 1)
	assert.Equal(t, "Given a", captures["Outline case"][0].Steps[0].Text)
	assert.Equal(t, "Given b", captures["Outline case"][1].Steps[0].Text)
}

func TestLoadCaptures_MissingDirIsEmptyNotError(t *testing.T) {
	captures, err := LoadCaptures("/no/such/dir")
	require.NoError(t, err)
	assert.Empty(t, captures)
}

func TestLoadCaptures_IgnoresNonJSONFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "readme.txt"), "not json")
	writeFile(t, filepath.Join(dir, "a-1.json"), `{"scenario":"Only case","steps":[]}`)

	captures, err := LoadCaptures(dir)
	require.NoError(t, err)
	require.Len(t, captures, 1)
	assert.Contains(t, captures, "Only case")
}

func TestLoadCaptures_FullStepFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a-1.json"), `{
		"scenario": "Full case",
		"feature": "features/x.feature",
		"tags": ["@doc"],
		"steps": [
			{"text": "the setup runs", "keyword": "Given", "status": "passed",
			 "cli_output": "ok", "mock_recorded": "=== Prompt ===", "materialized": "file contents"}
		]
	}`)

	captures, err := LoadCaptures(dir)
	require.NoError(t, err)
	require.Len(t, captures["Full case"], 1)
	step := captures["Full case"][0].Steps[0]
	assert.Equal(t, "Given", step.Keyword)
	assert.Equal(t, "passed", step.Status)
	assert.Equal(t, "ok", step.CLIOutput)
	assert.Equal(t, "=== Prompt ===", step.MockRecorded)
	assert.Equal(t, "file contents", step.Materialized)
}

func TestLoadCaptures_InvalidJSONErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bad.json"), `not json at all`)

	_, err := LoadCaptures(dir)
	assert.Error(t, err)
}

// TestDocCaptureIsTheSharedContract pins that this package's DocCapture and
// DocCaptureStep remain ALIASES of internal/shared/doccapture rather than
// re-declared copies. The reader (this package) and the writer
// (tests/acceptance/steps_doc_capture.go) sit on opposite sides of a process
// boundary and cannot import each other, so the two struct sets were once
// declared twice, byte-for-byte identical down to the json tags, held in sync
// only by a comment demanding it. internal/shared is the one tree both a
// tests/ package and a scripts/ package can import; re-declaring a local copy
// here would silently re-open the drift.
func TestDocCaptureIsTheSharedContract(t *testing.T) {
	if got, want := reflect.TypeOf(DocCapture{}), reflect.TypeOf(doccapture.DocCapture{}); got != want {
		t.Errorf("DocCapture is %v, not the shared %v — a local copy has been re-declared", got, want)
	}
	if got, want := reflect.TypeOf(DocCaptureStep{}), reflect.TypeOf(doccapture.DocCaptureStep{}); got != want {
		t.Errorf("DocCaptureStep is %v, not the shared %v — a local copy has been re-declared", got, want)
	}
	// Compile-time half: passing an argument is an assignment, and assignment
	// between two DISTINCT named struct types is illegal in Go even when their
	// underlying types are identical — so this call stops compiling the moment
	// either name stops being an alias.
	requireSharedContract(DocCapture{}, DocCaptureStep{})
}

// requireSharedContract accepts only internal/shared/doccapture's own types.
func requireSharedContract(doccapture.DocCapture, doccapture.DocCaptureStep) {}
