package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// pageStyle is emitted once near the top of every generated page. A raw
// <style> block in Starlight markdown passes through as global CSS. Two jobs:
//  1. Widen the content column (--sl-content-width) now that the page drops
//     its right-hand table of contents, while capping ordinary prose at a
//     comfortable reading measure so only the wide grid uses the room.
//  2. Define the step->output grid: two fixed columns (left = the cucumber
//     step, right = that step's captured output), collapsing to one stacked
//     column on narrow screens.
const pageStyle = `<style>
:root { --sl-content-width: 90rem; }
.sl-markdown-content :is(p, ul, ol, blockquote) { max-width: 52rem; }
.living-doc-grid {
  display: grid;
  grid-template-columns: minmax(0, 26rem) minmax(0, 1fr);
  gap: 0.4rem 1.5rem;
  align-items: start;
  margin: 0.75rem 0 1.75rem;
  max-width: none;
}
.living-doc-grid > .ldc { min-width: 0; overflow-x: auto; }
.living-doc-grid > .ldc > :first-child { margin-top: 0; }
.living-doc-grid > .ldc > :last-child { margin-bottom: 0; }
.living-doc-grid > .ldc:nth-child(odd) :is(pre, code, .ec-line, .code) {
  white-space: pre-wrap !important;
  overflow-wrap: anywhere;
  word-break: break-word;
}
@media (max-width: 720px) {
  .living-doc-grid { grid-template-columns: 1fr; gap: 0.2rem; }
}
</style>`

const (
	gridOpen  = `<div class="living-doc-grid">`
	cellOpen  = `<div class="ldc">`
	cellClose = `</div>`
	gridClose = `</div>`
)

const notCapturedMD = "> **Not captured in this build.** This scenario was not exercised in the " +
	"run that generated this page (for example, a `@live` scenario without " +
	"credentials in this environment). The Gherkin below is still the live " +
	"spec — just without a proof-of-passing run attached yet."

// RefusalError is returned when any step of any capture for a scenario did
// not pass. This is the load-bearing guarantee of the whole pipeline: a red
// scenario cannot be documented. GeneratePage returns this error instead of a
// page, and the caller must not write anything on this path.
type RefusalError struct {
	Feature  string
	Scenario string
	StepText string
	Status   string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf(
		"REFUSING TO GENERATE: scenario %q (%s) has a non-passed step (%q: %q) — a red scenario cannot be documented",
		e.Scenario, e.Feature, e.Status, e.StepText,
	)
}

var backtickRunRe = regexp.MustCompile("`+")

// safeFence returns a backtick fence at least one longer than the longest run
// of backticks already present in text, so captured content that itself
// contains a ```-fenced example (e.g. a command's own markdown) can never
// prematurely close the wrapping fence.
func safeFence(text string) string {
	longest := 0
	for _, run := range backtickRunRe.FindAllString(text, -1) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

func fenced(text, lang string) string {
	fence := safeFence(text)
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}

// cell wraps one grid cell. The blank lines around inner are load-bearing: a
// raw HTML block only yields back to the markdown parser after a blank line,
// so without them a fenced code block inside would render as literal text.
// inner may be empty (a step with no captured output).
func cell(inner string) []string {
	return []string{cellOpen, "", inner, "", cellClose}
}

// renderStepGrid renders the wide two-column body: one grid row per step,
// left cell = the cucumber step (with its reconstructed keyword so the
// gherkin grammar highlights it), right cell = that step's captured output.
func renderStepGrid(cap DocCapture) string {
	lines := []string{gridOpen}
	for _, step := range cap.Steps {
		gherkinLine := step.Text
		if step.Keyword != "" {
			gherkinLine = step.Keyword + " " + step.Text
		}
		left := fenced(gherkinLine, "gherkin")

		var rightParts []string
		if step.CLIOutput != "" {
			rightParts = append(rightParts, fenced(step.CLIOutput, "text"))
		}
		if step.MockRecorded != "" {
			rightParts = append(rightParts, fenced(step.MockRecorded, "text"))
		}
		if step.Materialized != "" {
			rightParts = append(rightParts, fenced(step.Materialized, "text"))
		}
		right := strings.Join(rightParts, "\n\n")

		lines = append(lines, cell(left)...)
		lines = append(lines, cell(right)...)
	}
	lines = append(lines, gridClose)
	return strings.Join(lines, "\n")
}

// renderChecklist renders the per-scenario header, ABOVE the grid: the
// provenance line plus a pass/fail mark for every step.
func renderChecklist(cap DocCapture) string {
	lines := []string{
		"Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.",
		"",
	}
	for _, step := range cap.Steps {
		mark := "✓"
		if step.Status != "passed" {
			mark = fmt.Sprintf("✗ (%s)", step.Status)
		}
		lines = append(lines, fmt.Sprintf("- %s %s", mark, step.Text))
	}
	return strings.Join(lines, "\n")
}

// EmptyCaptureError is returned when a capture EXISTS for a scenario but
// records no steps at all. Both honesty gates below are loops over cap.Steps,
// so such a capture satisfies both VACUOUSLY, and renderChecklist then prints
// "Every step below actually ran against a real ctxloom binary" above an empty
// list — a provenance claim about nothing. A scenario with no capture at all
// is a different, legitimate case (it renders the "not captured" notice); this
// is a capture that arrived empty.
type EmptyCaptureError struct {
	Feature  string
	Scenario string
}

func (e *EmptyCaptureError) Error() string {
	return fmt.Sprintf(
		"REFUSING TO GENERATE: scenario %q (%s) has a capture with ZERO steps — the pass and evidence checks would both succeed vacuously over it",
		e.Scenario, e.Feature,
	)
}

// assertAllPassed enforces the honesty rule: a capture with any step not
// "passed" refuses the whole page, not just that scenario. A capture with no
// steps at all is refused for the same reason — there is nothing to check, so
// checking proves nothing.
func assertAllPassed(featurePath, scenarioName string, captures []DocCapture) error {
	for _, cap := range captures {
		if len(cap.Steps) == 0 {
			return &EmptyCaptureError{Feature: featurePath, Scenario: scenarioName}
		}
		for _, step := range cap.Steps {
			if step.Status != "passed" {
				return &RefusalError{
					Feature:  featurePath,
					Scenario: scenarioName,
					StepText: step.Text,
					Status:   step.Status,
				}
			}
		}
	}
	return nil
}

// EvidenceGapError is returned when a scenario that actually ran and passed
// has an assertion step (governed by "Then") with NO captured evidence at
// all — CLIOutput, MockRecorded, and Materialized all empty. This is the
// second half of the "a feature that does not work cannot be documented"
// guarantee: a scenario every step of which is green, but whose assertions
// leave no observable trace, proves nothing to a reader — three journeys
// (J000400, J001700, J001800) shipped exactly this defect before this guard existed.
type EvidenceGapError struct {
	Feature  string
	Scenario string
	StepText string
	Example  int // 1-based Examples row within this scenario's captures; 0 when the scenario is not an Outline (a single capture)
}

func (e *EvidenceGapError) Error() string {
	suffix := ""
	if e.Example > 0 {
		suffix = fmt.Sprintf(" (Example %d)", e.Example)
	}
	return fmt.Sprintf(
		"REFUSING TO GENERATE: scenario %q (%s)%s has an assertion step with no captured evidence (%q) — a step that proves nothing cannot be documented",
		e.Scenario, e.Feature, suffix, e.StepText,
	)
}

// stepIsAssertion reports whether the step at index i is governed by "Then"
// — an assertion — as opposed to "Given"/"When" (setup/action). godog's
// pickle steps carry only a coarse Context/Action/Outcome type, and
// tests/acceptance/steps_doc_capture.go's gherkinKeyword collapses a run of
// same-type steps to "And" after the first (see that function's own doc
// comment) — so an "And" step's real governing keyword is whichever
// Given/When/Then most recently preceded it in THIS capture's own step list,
// not a fixed value.
//
// A step whose OWN keyword the capture side could not classify
// (empty — gherkinKeyword returns "" for any pickle type outside
// Context/Action/Outcome) used to be treated as non-assertion: "there is
// nothing to conservatively enforce evidence against." That is backwards —
// it silently turns the evidence gate off for exactly the input a godog
// version change or a new step type would produce, on the one guard that
// stops a proves-nothing scenario from being published. This step's own
// classification is unknown, not "known to be non-assertion", so it now
// fails closed with a named error instead of guessing.
func stepIsAssertion(steps []DocCaptureStep, i int) (bool, error) {
	if steps[i].Keyword == "" {
		return false, fmt.Errorf("step %d (%q) carries no Gherkin keyword — the capture side could not classify it, so this generator cannot tell assertion from setup/action", i, steps[i].Text)
	}
	governing := ""
	for j := 0; j <= i; j++ {
		switch steps[j].Keyword {
		case "Given", "When", "Then":
			governing = steps[j].Keyword
		}
	}
	return governing == "Then", nil
}

// hasEvidence reports whether a step captured ANY of the three evidence
// streams renderStepGrid draws its right-hand cell from.
func hasEvidence(step DocCaptureStep) bool {
	return step.CLIOutput != "" || step.MockRecorded != "" || step.Materialized != ""
}

// assertAllHaveEvidence enforces that guarantee: every "Then"-governed step
// (an And/But continuing a Then counts) of every capture for this scenario
// must carry at least one non-empty evidence stream. Given/When steps are
// exempt — they are setup and action; capturing their output is a bonus, not
// the load-bearing proof a reader needs the assertions to carry.
func assertAllHaveEvidence(featurePath, scenarioName string, captures []DocCapture) error {
	for idx, cap := range captures {
		for i, step := range cap.Steps {
			isAssertion, err := stepIsAssertion(cap.Steps, i)
			if err != nil {
				return fmt.Errorf("%s %q: %w", featurePath, scenarioName, err)
			}
			if !isAssertion || hasEvidence(step) {
				continue
			}
			example := 0
			if len(captures) > 1 {
				example = idx + 1
			}
			return &EvidenceGapError{
				Feature:  featurePath,
				Scenario: scenarioName,
				StepText: step.Text,
				Example:  example,
			}
		}
	}
	return nil
}

// renderScenario renders one scenario section: header, then either the
// checklist+grid for each capture (an Outline's Examples rows each get their
// own "Example N" block) or, with no capture at all, the raw Gherkin plus a
// "not captured" note. Narration prose for this scenario, if any, comes last.
func renderScenario(sc Scenario, narr Narration, captures []DocCapture) string {
	var out []string
	out = append(out, "## "+sc.Name, "")
	if len(sc.Tags) > 0 {
		out = append(out, "*Tags: "+strings.Join(sc.Tags, " ")+"*", "")
	}

	if len(captures) > 0 {
		for idx, cap := range captures {
			if len(captures) > 1 {
				out = append(out, fmt.Sprintf("**Example %d**", idx+1), "")
			}
			out = append(out, renderChecklist(cap), "")
			out = append(out, renderStepGrid(cap), "")
		}
	} else {
		out = append(out, fenced(sc.Body, "gherkin"), "")
		out = append(out, notCapturedMD, "")
	}

	if prose, ok := narr.Scenarios[sc.Name]; ok && prose != "" {
		out = append(out, prose, "")
	}
	return strings.Join(out, "\n")
}

// GeneratePage renders one journey's full Starlight page from its parsed
// feature, its (possibly empty) narration, and every capture observed across
// the whole capture directory. narrationPath is used only for the generated
// banner comment; pass "" when no .doc.md companion exists for this feature.
//
// Returns a *RefusalError (see assertAllPassed) if any scenario's capture has
// a non-passed step, or an *EvidenceGapError (see assertAllHaveEvidence) if a
// passing scenario's assertion step has no captured evidence at all — the
// caller must not write the returned page in either case (there isn't one;
// the string is empty).
func GeneratePage(feat Feature, narr Narration, capturesByName map[string][]DocCapture, narrationPath string) (string, error) {
	var lines []string

	lines = append(lines,
		"---",
		fmt.Sprintf("title: %s", strconv.Quote(feat.Name)),
		// This journey page is intentionally WIDE: the step->output grid needs
		// horizontal room, so the right-hand table of contents is dropped (see
		// the widened --sl-content-width in pageStyle below).
		"tableOfContents: false",
		"---",
	)

	sources := "tests/acceptance/features/" + filepath.Base(feat.Path)
	if narrationPath != "" {
		sources += " + " + filepath.Base(narrationPath)
	}
	lines = append(lines,
		"<!-- GENERATED by scripts/gendocs/livingdocs (`just gen-living-docs`) from "+sources+",",
		"     using evidence captured from a PASSING acceptance run. Do not hand-edit;",
		"     edit the narration companion (if any) or the .feature file and regenerate. -->",
		pageStyle,
		"",
		":::note",
		fmt.Sprintf(
			"This page is generated from a Gherkin acceptance journey (`%s`) plus real "+
				"terminal output captured from an actual passing run of it — not "+
				"hand-written. See [the living-docs proposal](https://github.com/ctxloom/ctxloom/blob/main/docs/living-docs-plan.md) for how.",
			filepath.Base(feat.Path),
		),
		":::",
		"",
	)

	if narr.Intro != "" {
		lines = append(lines, narr.Intro, "")
	}
	if len(feat.Description) > 0 {
		lines = append(lines, "> "+strings.Join(feat.Description, "\n> "), "")
	}

	for _, sc := range feat.Scenarios {
		caps := capturesByName[sc.Name]
		if err := assertAllPassed(feat.Path, sc.Name, caps); err != nil {
			return "", err
		}
		if err := assertAllHaveEvidence(feat.Path, sc.Name, caps); err != nil {
			return "", err
		}
		lines = append(lines, renderScenario(sc, narr, caps))
	}

	if narr.Outro != "" {
		lines = append(lines, narr.Outro, "")
	}

	return strings.Join(lines, "\n"), nil
}
