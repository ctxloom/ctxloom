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
// contains a ```-fenced example (e.g. a skill's own markdown) can never
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

// assertAllPassed enforces the honesty rule: a capture with any step not
// "passed" refuses the whole page, not just that scenario.
func assertAllPassed(featurePath, scenarioName string, captures []DocCapture) error {
	for _, cap := range captures {
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
// a non-passed step — the caller must not write the returned page in that
// case (there isn't one; the string is empty).
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
		lines = append(lines, renderScenario(sc, narr, caps))
	}

	if narr.Outro != "" {
		lines = append(lines, narr.Outro, "")
	}

	return strings.Join(lines, "\n"), nil
}
