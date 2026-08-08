package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func passingFeature() Feature {
	return Feature{
		Path:        "tests/acceptance/features/j2_setup.feature",
		Name:        "Setting up a project",
		Description: []string{"A description line."},
		Tags:        []string{"@doc"},
		Scenarios: []Scenario{
			{Name: "First scenario", Body: "Scenario: First scenario\n    Given a precondition"},
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

	page, err := GeneratePage(feat, narr, captures, "j2_setup.doc.md")
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

// A step whose OWN keyword the capture side could not classify
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

// TestGeneratePage_RefusesZeroStepCapture pins that a capture with no steps is
// refused rather than published. Both honesty gates are loops over cap.Steps,
// so a zero-step capture satisfied BOTH vacuously — and renderChecklist then
// printed "Every step below actually ran against a real `ctxloom` binary"
// above an empty list, publishing a provenance claim about nothing.
//
// The producer of such a file is already blocked (flushDocCapture refuses a
// zero-step capture), but LoadCaptures reads whatever *.json
// sits in the capture directory — a file left by an older build, or truncated
// — so the reader must not be the half that trusts.
func TestGeneratePage_RefusesZeroStepCapture(t *testing.T) {
	feat := Feature{Path: "f.feature", Name: "F", Scenarios: []Scenario{{Name: "S", Body: "Scenario: S"}}}
	caps := map[string][]DocCapture{"S": {{Scenario: "S", Steps: nil}}}

	page, err := GeneratePage(feat, Narration{}, caps, "")
	if err == nil {
		t.Fatalf("GeneratePage accepted a zero-step capture and returned a page:\n%s", page)
	}
	if page != "" {
		t.Errorf("GeneratePage returned %d bytes alongside the refusal; the caller must have nothing to write", len(page))
	}
	if !strings.Contains(err.Error(), "S") {
		t.Errorf("refusal does not name the scenario: %v", err)
	}
}

// TestGeneratePage_NoCaptureIsStillNotAnError guards the boundary the check
// above must not cross: a scenario with NO capture at all is the legitimate
// "not captured in this build" path and stays a page, not a refusal.
func TestGeneratePage_NoCaptureIsStillNotAnError(t *testing.T) {
	feat := Feature{Path: "f.feature", Name: "F", Scenarios: []Scenario{{Name: "S", Body: "Scenario: S"}}}
	page, err := GeneratePage(feat, Narration{}, map[string][]DocCapture{}, "")
	if err != nil {
		t.Fatalf("GeneratePage: %v", err)
	}
	if !strings.Contains(page, "Not captured in this build") {
		t.Errorf("uncaptured scenario lost its notice:\n%s", page)
	}
}

// TestHasEvidence_IsAPresenceCheckNotAQualityJudgement pins hasEvidence's
// contract deliberately: a narrower, shape-based check was once proposed and
// the narrowing is wrong.
//
// The gate asks one question — did this step capture ANY of the three evidence
// streams? — and nothing else. It is not a quality judgement, and it must not
// become one: a terse capture like "exit=0" from a command that legitimately
// prints nothing is real, run-produced proof, while any length or shape
// heuristic that rejected it would refuse to publish a true page. The claim
// that "several acceptance steps set exactly that shape" does not hold
// — every exit= evidence site in tests/acceptance appends the command's own
// output ("exit=%d\n%s").
func TestHasEvidence_IsAPresenceCheckNotAQualityJudgement(t *testing.T) {
	assert.False(t, hasEvidence(DocCaptureStep{}), "no streams at all is the only absence")

	for name, step := range map[string]DocCaptureStep{
		"cli only":        {CLIOutput: "exit=0"},
		"mock only":       {MockRecorded: "x"},
		"materialized":    {Materialized: "exit=0"},
		"all three":       {CLIOutput: "a", MockRecorded: "b", Materialized: "c"},
		"single space":    {CLIOutput: " "},
		"terse but real":  {Materialized: "signature_state: trusted"},
		"multiline usual": {CLIOutput: "exit=0\nhello\n"},
	} {
		assert.Truef(t, hasEvidence(step), "%s must satisfy the evidence gate", name)
	}
}
