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
	path := filepath.Join(dir, "j000200_setup.feature")
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

// TestParseFeature_TaggedExamplesStayInTheBody pins that a @tag line ends a
// scenario only when a NEW scenario follows it. Per-row tagging in Gherkin
// attaches tags to an Examples: block (tags cannot attach to a single table
// row), and this repo uses that shape — see
// tests/acceptance/features/isolation_probe.feature, which has ten
// individually-tagged one-row Examples blocks. Treating every @tag as a
// terminator truncated such a scenario's Body at the first tag, so the raw
// Gherkin published for an uncaptured scenario silently lost its Examples
// tables — the only place Body is rendered.
func TestParseFeature_TaggedExamplesStayInTheBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.feature")
	writeFile(t, path, `@doc
Feature: Probe

  @live
  Scenario Outline: The probe runs for <engine>
    Given the probe targets "<engine>"
    Then it holds for "<engine>"

    @claude-code @worktree
    Examples:
      | engine      |
      | claude-code |

    @codex @container
    Examples:
      | engine |
      | codex  |

  @wip
  Scenario: A later scenario
    Given something else
`)

	feat, err := ParseFeature(path)
	require.NoError(t, err)
	require.Len(t, feat.Scenarios, 2, "the tagged Examples blocks must not split the outline into extra scenarios")

	outline := feat.Scenarios[0]
	assert.Equal(t, "The probe runs for <engine>", outline.Name)
	assert.Equal(t, []string{"@live"}, outline.Tags)
	assert.Contains(t, outline.Body, "@claude-code @worktree", "the Examples block's own tags belong to the outline")
	assert.Contains(t, outline.Body, "| claude-code |")
	assert.Contains(t, outline.Body, "@codex @container")
	assert.Contains(t, outline.Body, "| codex  |")
	assert.NotContains(t, outline.Body, "A later scenario")
	assert.NotContains(t, outline.Body, "@wip", "the NEXT scenario's tags must still terminate this one")

	assert.Equal(t, "A later scenario", feat.Scenarios[1].Name)
	assert.Equal(t, []string{"@wip"}, feat.Scenarios[1].Tags)
}

// TestParseFeature_CommentsAndDanglingTagsInBody characterizes two arms of the
// scenario-body loop that nothing else reached: a '#' comment INSIDE a
// scenario (dropped from Body, since the published Gherkin is the spec, not
// the source), and a run of tags at END OF FILE (which belongs to no scenario
// and therefore terminates the last one rather than joining its body).
func TestParseFeature_CommentsAndDanglingTagsInBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.feature")
	writeFile(t, path, `@doc
Feature: Comments

  Scenario: Has a comment inside
    Given a step
    # an internal comment
    Then another step

  @dangling @at-eof
`)
	feat, err := ParseFeature(path)
	require.NoError(t, err)
	require.Len(t, feat.Scenarios, 1)

	body := feat.Scenarios[0].Body
	assert.Contains(t, body, "Given a step")
	assert.Contains(t, body, "Then another step")
	assert.NotContains(t, body, "an internal comment", "comments are source, not spec")
	assert.NotContains(t, body, "@dangling", "a tag run at end of file belongs to no scenario")
}
