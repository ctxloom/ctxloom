package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestParseFeature_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j1_setup.feature")
	writeFile(t, path, `@doc
Feature: Setting up a project

  A short description line.
  A second description line.

  # a comment, ignored
  Scenario: First scenario
    Given a precondition
    When an action happens
    Then an outcome follows

  @wip
  Scenario: Second scenario
    Given something else
`)

	feat, err := ParseFeature(path)
	require.NoError(t, err)

	assert.Equal(t, "Setting up a project", feat.Name)
	assert.Equal(t, []string{"@doc"}, feat.Tags)
	assert.Equal(t, []string{"A short description line.", "A second description line."}, feat.Description)
	require.Len(t, feat.Scenarios, 2)

	sc1 := feat.Scenarios[0]
	assert.Equal(t, "First scenario", sc1.Name)
	assert.Empty(t, sc1.Tags)
	assert.Contains(t, sc1.Body, "Given a precondition")
	assert.Contains(t, sc1.Body, "Then an outcome follows")

	sc2 := feat.Scenarios[1]
	assert.Equal(t, "Second scenario", sc2.Name)
	assert.Equal(t, []string{"@wip"}, sc2.Tags)
}

func TestParseFeature_ScenarioOutlineKeepsExamplesInBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outline.feature")
	writeFile(t, path, `@doc
Feature: An outline feature

  Scenario Outline: Parameterized
    Given a value <value>

    Examples:
      | value |
      | one   |
      | two   |
`)

	feat, err := ParseFeature(path)
	require.NoError(t, err)
	require.Len(t, feat.Scenarios, 1)
	sc := feat.Scenarios[0]
	assert.Contains(t, sc.Body, "Examples:")
	assert.Contains(t, sc.Body, "| one   |")
}

func TestParseFeature_NoFeatureLineErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.feature")
	writeFile(t, path, "# just a comment\n")

	_, err := ParseFeature(path)
	assert.Error(t, err)
}

func TestParseFeature_MissingFileErrors(t *testing.T) {
	_, err := ParseFeature("/no/such/path.feature")
	assert.Error(t, err)
}

func TestDiscoverDocFeatures(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a_tagged.feature"), "@doc\nFeature: Tagged\n\n  Scenario: S\n    Given x\n")
	writeFile(t, filepath.Join(dir, "b_untagged.feature"), "Feature: Untagged\n\n  Scenario: S\n    Given x\n")
	writeFile(t, filepath.Join(dir, "c_tagged.feature"), "@doc @other\nFeature: Also tagged\n\n  Scenario: S\n    Given x\n")
	writeFile(t, filepath.Join(dir, "notes.txt"), "not a feature file")

	found, err := DiscoverDocFeatures(dir)
	require.NoError(t, err)

	require.Len(t, found, 2)
	assert.Equal(t, filepath.Join(dir, "a_tagged.feature"), found[0])
	assert.Equal(t, filepath.Join(dir, "c_tagged.feature"), found[1])
}

func TestDiscoverDocFeatures_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	found, err := DiscoverDocFeatures(dir)
	require.NoError(t, err)
	assert.Empty(t, found)
}
