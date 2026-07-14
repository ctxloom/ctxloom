package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
