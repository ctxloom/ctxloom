package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func passingFeature() Feature {
	return Feature{
		Path:        "tests/acceptance/features/j000200_setup.feature",
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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Keyword: "Given", Status: "passed", CLIOutput: "ok"}}},
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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "a precondition", Keyword: "Given", Status: "passed", CLIOutput: "ok"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "j000200_setup.doc.md")
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
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row one", Keyword: "Given", Status: "passed", CLIOutput: "ok"}}},
			{Scenario: "First scenario", Steps: []DocCaptureStep{{Text: "row two", Keyword: "Given", Status: "passed", CLIOutput: "ok"}}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "**Example 1**")
	assert.Contains(t, page, "**Example 2**")
}

// TestGeneratePage_CommandRunEarlierProvesTheScenario_AssertionStepsRunNoCommand
// pins the case this fix exists for: real journeys (e.g.
// j001000_transcript_capture.feature's "A prior claude session's native log
// becomes the canonical transcript you own") run a command in a When step,
// then assert its effects in one or more Then steps that read state off disk
// or check an exit code -- no command, so no CLIOutput/MockRecorded/
// Materialized on THOSE steps. Evidence belongs to the scenario, not to
// whichever step happened to run the command: the When step's captured
// output is what proves the assertions true, and the page must render with
// no error. This is the scenario that was wrongly refused before this fix --
// under the old per-step rule, "an assertion with nothing to show" alone
// would have failed the whole page.
func TestGeneratePage_CommandRunEarlierProvesTheScenario_AssertionStepsRunNoCommand(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "I run \"ctxloom session backfill amber-claude-harp\"", Keyword: "When", Status: "passed", CLIOutput: "backfilled 4 turns"},
				{Text: "the canonical transcript replays the fixture's real turns", Keyword: "Then", Status: "passed"},
				{Text: "every line is stamped engine \"claude-code\"", Keyword: "And", Status: "passed"},
			}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err, "a scenario that ran a command and then asserted its effects has evidence at the scenario level")
	assert.Contains(t, page, "backfilled 4 turns", "the captured command output must still appear on the page")
	assert.Contains(t, page, "the canonical transcript replays the fixture's real turns")
}

// TestGeneratePage_RefusesWhenScenarioCapturedNoEvidenceAnywhere pins the
// other half: a scenario where NOTHING in the whole run -- no step, of any
// keyword -- ever captured CLIOutput/MockRecorded/Materialized still refuses.
// Evidence moved from per-step to per-scenario, but a scenario that proves
// nothing must still not be published as though it did.
func TestGeneratePage_RefusesWhenScenarioCapturedNoEvidenceAnywhere(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "an action that captured nothing", Keyword: "When", Status: "passed"},
				{Text: "an assertion with nothing to show", Keyword: "Then", Status: "passed"},
			}},
		},
	}

	_, err := GeneratePage(feat, narr, captures, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "First scenario")
	assert.Contains(t, err.Error(), feat.Path)
	assert.Contains(t, err.Error(), "proves nothing")

	var gap *EvidenceGapError
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, "First scenario", gap.Scenario)
	assert.Equal(t, 0, gap.Example)
}

func TestGeneratePage_GivenAndWhenStepsAloneCanCarryTheEvidence(t *testing.T) {
	feat := passingFeature()
	narr := Narration{Scenarios: map[string]string{}}
	captures := map[string][]DocCapture{
		"First scenario": {
			{Scenario: "First scenario", Steps: []DocCaptureStep{
				{Text: "a precondition", Keyword: "Given", Status: "passed"},
				{Text: "an And continuing the Given", Keyword: "And", Status: "passed"},
				{Text: "an action", Keyword: "When", Status: "passed", Materialized: "the real payload"},
				{Text: "the one assertion", Keyword: "Then", Status: "passed"},
			}},
		},
	}

	page, err := GeneratePage(feat, narr, captures, "")
	require.NoError(t, err)
	assert.Contains(t, page, "the real payload")
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
