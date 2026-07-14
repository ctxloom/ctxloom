package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func passingFeature() Feature {
	return Feature{
		Path:        "tests/acceptance/features/j1_setup.feature",
		Name:        "Setting up a project",
		Description: []string{"A description line."},
		Tags:        []string{"@doc"},
		Scenarios: []Scenario{
			{Keyword: "Scenario", Name: "First scenario", Body: "Scenario: First scenario\n    Given a precondition"},
		},
	}
}

func TestGeneratePage_AllPassedRendersGrid(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed", CLIOutput: "ok output"},
			}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)

	assert.Contains(t, page, `title: "Setting up a project"`)
	assert.Contains(t, page, "living-doc-grid")
	assert.Contains(t, page, "Given a precondition")
	assert.Contains(t, page, "ok output")
	assert.Contains(t, page, "✓ a precondition")
	assert.NotContains(t, page, "Not captured in this build")
}

func TestGeneratePage_RefusesOnNonPassedStep(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Status: "failed"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "First scenario")
	assert.Contains(t, err.Error(), "failed")

	var refusal *RefusalError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "First scenario", refusal.Scenario)
}

func TestGeneratePage_NoCaptureRendersGherkinOnlyNotAnError(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{} // nothing captured for this scenario

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)

	assert.Contains(t, page, "Not captured in this build")
	assert.Contains(t, page, "Given a precondition")
	assert.NotContains(t, page, `<div class="living-doc-grid">`)
}

func TestGeneratePage_NarrationOptional(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}} // no .doc.md loaded
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Status: "passed"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "First scenario")
}

func TestGeneratePage_IncludesNarrationProseWhenPresent(t *testing.T) {
	feat := passingFeature()
	narr := Narration{
		Intro:     "Intro prose.",
		Outro:     "Outro prose.",
		Scenarios: map[string]string{"First scenario": "Scenario-specific prose."},
	}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Status: "passed"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "j1_setup.doc.md")
	require.NoError(t, err)
	assert.Contains(t, page, "Intro prose.")
	assert.Contains(t, page, "Outro prose.")
	assert.Contains(t, page, "Scenario-specific prose.")
}

func TestGeneratePage_MultipleExamplesLabeled(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row one", Status: "passed"}}},
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row two", Status: "passed"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "**Example 1**")
	assert.Contains(t, page, "**Example 2**")
}

func TestSafeFence_LongerThanContentBackticks(t *testing.T) {
	assert.Equal(t, "```", safeFence("no backticks here"))
	assert.Equal(t, "````", safeFence("has ``` triple backticks"))
	assert.Equal(t, "```````", safeFence("has `````` six backticks"))
}

func TestFenced_WrapsWithSafeFence(t *testing.T) {
	out := fenced("some text", "gherkin")
	assert.Contains(t, out, "```gherkin\nsome text\n```")
}
