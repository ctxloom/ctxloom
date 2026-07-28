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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Keyword: "Given", Status: "passed"}}},
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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Keyword: "Given", Status: "passed"}}},
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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row one", Keyword: "Given", Status: "passed"}}},
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row two", Keyword: "Given", Status: "passed"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "**Example 1**")
	assert.Contains(t, page, "**Example 2**")
}

func TestGeneratePage_RefusesOnThenStepWithNoEvidence(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "an assertion with nothing to show", Keyword: "Then", Status: "passed"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "First scenario")
	assert.Contains(t, err.Error(), "an assertion with nothing to show")
	assert.Contains(t, err.Error(), "proves nothing")

	var gap *EvidenceGapError
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, "First scenario", gap.Scenario)
	assert.Equal(t, 0, gap.Example)
}

func TestGeneratePage_AndContinuingAThenAlsoRequiresEvidence(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "the first assertion", Keyword: "Then", Status: "passed", CLIOutput: "real output"},
				{Text: "a second assertion with nothing to show", Keyword: "And", Status: "passed"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a second assertion with nothing to show")
}

func TestGeneratePage_GivenAndWhenStepsExemptFromEvidenceRequirement(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "an And continuing the Given", Keyword: "And", Status: "passed"},
				{Text: "an action", Keyword: "When", Status: "passed"},
				{Text: "the one assertion", Keyword: "Then", Status: "passed", Materialized: "the real payload"},
			}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "the real payload")
}

// U157-F01: a step whose OWN keyword the capture side could not classify
// (empty -- steps_doc_capture.go's gherkinKeyword returns "" for any pickle
// type outside Context/Action/Outcome, e.g. a godog version change or an
// unrecognized step type) used to be silently treated as non-assertion --
// exempt from the evidence gate, the one guard that stops a proves-nothing
// scenario from being published. Proves it now fails closed with a named
// error instead of guessing "not an assertion".
func TestGeneratePage_UnclassifiableStepKeywordFailsClosed(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "a step of some new unrecognized pickle type", Keyword: "", Status: "passed"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err, "an unclassifiable step must not be silently exempted from the evidence gate")
	assert.Contains(t, err.Error(), "a step of some new unrecognized pickle type")
}

func TestGeneratePage_EvidenceGapNamesTheExampleRowForOutlines(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "row one assertion", Keyword: "Then", Status: "passed", CLIOutput: "ok"},
			}},
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "row two assertion", Keyword: "Then", Status: "passed"}, // no evidence
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err)

	var gap *EvidenceGapError
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, 2, gap.Example)
	assert.Contains(t, err.Error(), "Example 2")
}

func TestGeneratePage_MockRecordedOrMaterializedAloneSatisfyEvidence(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "an assertion evidenced only by the mock", Keyword: "Then", Status: "passed", MockRecorded: "recorded prompt"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
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
