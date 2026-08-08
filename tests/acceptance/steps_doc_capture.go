//go:build acceptance

// Package acceptance: the @doc capture sidecar.
//
// This is a PROTOTYPE seam for a proposed "living docs" pipeline (see
// docs/living-docs-plan.md at the repo root of this worktree). It is NOT
// part of the ordinary `just test-acceptance` run — no plain acceptance
// invocation sets CTXLOOM_DOC_CAPTURE_DIR — but it IS wired into CI: `just
// gen-living-docs` (invoked by .github/workflows/docs.yml's "Generate
// living-docs journey pages" step) sets CTXLOOM_DOC_CAPTURE_DIR, runs the
// acceptance suite with capture on, then renders
// website/src/content/docs/journeys/ from that run's real captured output
// (this comment used to claim "not wired into … CI" outright, which stopped
// being true once docs.yml started calling gen-living-docs).
// It is inert unless CTXLOOM_DOC_CAPTURE_DIR is set, in which case every step
// of every
// @doc-tagged scenario has its real, run-produced evidence — CLI stdout/
// stderr and, where applicable, the mock engine's recorded input (the
// "=== Prompt ===" payload from internal/lm/backends/mock.go) — routed
// through godog's NATIVE cucumber-message attachment channel
// (godog.Attach/godog.Attachments, confirmed present in
// github.com/cucumber/godog v0.15.1: see attachment_test.go and
// internal/formatters/fmt_cucumber.go's "embeddings" support) and ALSO
// flushed directly to a per-scenario JSON file for a generator to consume —
// so this prototype does not additionally require wiring a
// "cucumber:<file>.json" formatter into the suite's Options.Format just to
// exercise the same evidence.
//
// A red (failing) scenario never reaches ctx.After un-erred in a state worth
// publishing; flushDocCapture still writes the file so a broken scenario's
// capture is visibly marked failed rather than silently absent, but the
// generator (scripts/gendocs/livingdocs) refuses to render a page
// from any capture whose steps are not ALL "passed" — that is the "a red
// scenario cannot be documented" guarantee.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/shared/doccapture"
)

// docCapture and docCaptureStep are this package's names for the shared
// living-docs wire contract (internal/shared/doccapture) both this writer and
// scripts/gendocs/livingdocs's reader use (these used to be two
// independent, byte-for-byte-identical struct declarations, held in sync only
// by a comment demanding it). Aliased, not copied, so every existing
// w.docCapture.Steps-style field access below is unaffected.
type docCapture = doccapture.DocCapture
type docCaptureStep = doccapture.DocCaptureStep

// gherkinKeyword reconstructs a Gherkin keyword from a pickle step's Type.
// Pickles drop the authored keyword and keep only a coarse Type (Context /
// Action / Outcome), so And/But — which inherit the governing keyword's Type —
// are recovered by collapsing a run of same-type steps to "And" after the
// first. For these features that reproduces the authored keywords verbatim
// (they use Given/When/Then + And, no But). Unknown-type steps get no keyword.
func gherkinKeyword(stepType, prevType string) string {
	primary := map[string]string{"Context": "Given", "Action": "When", "Outcome": "Then"}
	kw, ok := primary[stepType]
	if !ok {
		return ""
	}
	if stepType == prevType {
		return "And"
	}
	return kw
}

// isOutcomeStep reports whether a step is Then-governed: a Then, or an And/But
// continuing a run of Thens. Gherkin gives a continuation the previous step's
// type, so the pickle type alone is the signal; the rendered keyword is used
// only to keep this readable at the call site.
func isOutcomeStep(stepType, keyword string) bool {
	return stepType == "Outcome" && (keyword == "Then" || keyword == "And" || keyword == "But")
}

// scenarioIDRe turns a scenario name (+ Examples row, for Outlines) into a
// filesystem-safe capture filename.
var scenarioIDRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func scenarioFileName(sc *godog.Scenario) string {
	slug := strings.Trim(scenarioIDRe.ReplaceAllString(strings.ToLower(sc.Name), "-"), "-")
	// godog gives every Examples row of an Outline the same pickle Name; the
	// row's own values (e.g. "claude-code") appear in its steps' rendered
	// text, not in sc.Name, so disambiguate multiple rows with the pickle Id
	// (unique per row) rather than colliding on one file.
	return slug + "-" + sc.Id + ".json"
}

// tempRandomSuffixRe strips a leftover MkdirTemp random-number suffix (e.g.
// "fake-companion-238940123" -> "fake-companion") from any fixture directory
// this scrub doesn't otherwise know the shape of, once its enclosing temp
// root has already been collapsed to a stable placeholder below. Without
// this, an unanticipated fixture dir would still churn a random digit string
// into the published page on every regeneration.
var tempRandomSuffixRe = regexp.MustCompile(`-\d{4,}\b`)

// scrubTempPaths replaces every throwaway hermetic path this scenario's
// TestEnvironment created — the project checkout, Bob's separate clone, and
// any bare-git remote/team-origin fixture root, all of which live under one
// freshly-random-named /tmp/ctxloom-integration-<N> per run — with stable
// placeholders in captured evidence text, so the published page reads the
// same on every regeneration instead of leaking a throwaway absolute path.
//
// Order matters: the two-level fixture shapes (remote/team-origin) are
// collapsed FIRST, to a placeholder that keeps the meaningful item-ref suffix
// intact (e.g. "<remote>.git@bundles/secure-coding#fragments/guidance" — the
// ref itself, not just the path, is the evidence on the trust pages). Named
// single-purpose dirs (project, Bob's checkout, home) go next. Only then does
// the scenario's whole temp root collapse to a generic "<tmp>" placeholder,
// as a safety net for any fixture dir this function doesn't yet know by
// shape — never the reverse, or the specific patterns above would no longer
// match once their shared root prefix was already gone.
//
// Capture-time only: it runs after a step's assertions have already passed or
// failed, so it can never change what was checked — only how the
// already-decided evidence is displayed on the published page.
func scrubTempPaths(w *World, s string) string {
	if s == "" || w == nil || w.env == nil || w.env.Root == "" {
		return s
	}
	root := regexp.QuoteMeta(w.env.Root)

	// Anchored to THIS scenario's root, and consuming it whole, so no bare
	// "<tmp>" fragment is left dangling in front of the placeholder below —
	// the two-level fixture shapes collapse to a single, clean token.
	s = regexp.MustCompile(root+`/remote-\d+/remote\.git`).ReplaceAllString(s, "<remote>.git")
	s = regexp.MustCompile(root+`/j7-origin-\d+/team\.git`).ReplaceAllString(s, "<team-origin>.git")

	if w.j7s != nil && w.j7s.bobDir != "" {
		s = strings.ReplaceAll(s, w.j7s.bobDir, "<bob-checkout>")
	}
	if w.env.ProjectDir != "" {
		s = strings.ReplaceAll(s, w.env.ProjectDir, "/project")
	}
	if w.env.HomeDir != "" {
		s = strings.ReplaceAll(s, w.env.HomeDir, "<home>")
	}

	if strings.Contains(s, w.env.Root) {
		s = strings.ReplaceAll(s, w.env.Root, "<tmp>")
		s = tempRandomSuffixRe.ReplaceAllString(s, "")
	}
	return s
}

func hasDocTag(sc *godog.Scenario) bool {
	for _, t := range sc.Tags {
		if t.Name == "@doc" {
			return true
		}
	}
	return false
}

// registerDocCaptureHooks wires the capture sidecar. A no-op unless
// CTXLOOM_DOC_CAPTURE_DIR is set, so every other suite run (`just
// test-acceptance`, CI) pays nothing and behaves exactly as before this file
// existed.
func registerDocCaptureHooks(ctx *godog.ScenarioContext) {
	dir := os.Getenv("CTXLOOM_DOC_CAPTURE_DIR")
	if dir == "" {
		return
	}

	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		if !hasDocTag(sc) {
			return c, nil
		}
		w := worldFrom(c)
		if w == nil {
			return c, nil
		}
		tags := make([]string, 0, len(sc.Tags))
		for _, t := range sc.Tags {
			tags = append(tags, t.Name)
		}
		w.docCapture = &docCapture{Scenario: sc.Name, Feature: sc.Uri, Tags: tags}
		w.docFileName = scenarioFileName(sc)
		// Per-scenario reset of the cross-step trackers so one scenario's tail
		// state never bleeds into the next.
		w.docPrevStepType = ""
		w.docLastBobOutput = ""
		w.docLastCommandOutput = ""
		w.docStepMaterialized = ""
		w.docLastToolCalls = w.toolCalls
		if w.env != nil {
			w.docLastRunCount = w.env.RunCount()
		}
		return c, nil
	})

	ctx.StepContext().After(func(c context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.docCapture == nil {
			return c, nil
		}
		step := docCaptureStep{Text: st.Text, Status: status.String()}
		step.Keyword = gherkinKeyword(string(st.Type), w.docPrevStepType)
		w.docPrevStepType = string(st.Type)

		if w.env != nil {
			// A step that RAN a command owns that command's output.
			// env.RunCount() advances per invocation, so this credits "a
			// command ran, even one whose output is identical to the previous
			// step's (two materialize calls)" while a step that ran nothing
			// does not silently inherit a stale LastOutput. A string-equality
			// guard cannot draw that line; the counter can.
			if rc := w.env.RunCount(); rc != w.docLastRunCount {
				if out := w.env.LastOutput(); out != "" {
					step.CLIOutput = out
					w.docLastCommandOutput = out
				}
				w.docLastRunCount = rc
			} else if tc := w.toolCalls; tc != w.docLastToolCalls {
				// The MCP channel, same rule as the CLI one: a step that
				// INVOKED a tool owns its result as evidence. Without this,
				// every assertion in a tool-driven scenario is undocumentable
				// for the same reason a CLI one would be.
				if out := w.lastTool.JSON(); out != "" {
					step.CLIOutput = out
					w.docLastCommandOutput = out
				}
				w.docLastToolCalls = tc
			} else if isOutcomeStep(string(st.Type), step.Keyword) {
				// A Then INHERITS the output of the command it is asserting
				// about. `Then the command succeeds` runs nothing itself, and
				// an assertion that reads a file rather than stdout runs
				// nothing either — but both are claims ABOUT the invocation
				// that just happened, and its output is the evidence a reader
				// needs beside them. Without this, any assertion phrased on
				// state rather than output is undocumentable, which excludes
				// every scenario whose proof is a file on disk.
				//
				// Bounded on both sides so this cannot become the stale-output
				// hole the counters exist to prevent: docLastCommandOutput is
				// cleared per scenario, and replaced the moment the next
				// invocation happens. A Then can only ever inherit from an
				// invocation in its own scenario, and only the most recent one.
				step.CLIOutput = w.docLastCommandOutput
			}
		}
		// J7 runs the teammate's (Bob's) commands in a separate checkout via its
		// own exec plumbing, so that output never touches w.env.LastOutput() or
		// its run counter. Surface it on a new-since-last-step basis so each
		// teammate-side step shows its real output rather than inheriting a
		// prior step's.
		if w.j7s != nil {
			if bob := w.j7s.bobOutput; bob != "" && bob != w.docLastBobOutput {
				if step.CLIOutput == "" {
					step.CLIOutput = bob
				} else {
					step.CLIOutput = step.CLIOutput + "\n" + bob
				}
				w.docLastBobOutput = bob
			}
		}
		// Set-and-consume evidence a step observed off w.env's streams entirely
		// (a file it read, a captured PTY session, a sync notice a later
		// materialize overwrote). The step that produced it set it during its
		// own execution; attach it here and clear so it never bleeds onward.
		if w.docStepMaterialized != "" {
			step.Materialized = w.docStepMaterialized
			w.docStepMaterialized = ""
		}
		// Whichever mock-recorded slot this scenario populated most recently —
		// j2_setup's restart-delivery scenario uses j2RestartRecorded, j3's
		// discovery-interview scenarios use j3Recorded. Attach whichever is
		// non-empty and hasn't already been attached to an earlier step (avoid
		// repeating the same payload on every subsequent step once it's set).
		if w.j2RestartRecorded != "" && w.j2RestartRecorded != w.docLastMockRecorded {
			step.MockRecorded = w.j2RestartRecorded
			w.docLastMockRecorded = w.j2RestartRecorded
		} else if w.j3Recorded != "" && w.j3Recorded != w.docLastMockRecorded {
			step.MockRecorded = w.j3Recorded
			w.docLastMockRecorded = w.j3Recorded
		}
		// Scrub every throwaway hermetic path this scenario's TestEnvironment
		// created (project checkout, Bob's separate clone, bare-git remote/
		// team-origin fixture roots, the whole temp root as a fallback) to
		// stable placeholders, so the published page reads the same on every
		// regeneration instead of leaking a random /tmp path. Applied to all
		// three evidence streams — CLI output, materialized-file evidence, and
		// the mock engine's recorded prompt all carry the temp root verbatim.
		// Capture-time only: never touches assertion logic or the real
		// directory ctxloom actually wrote to.
		step.CLIOutput = scrubTempPaths(w, step.CLIOutput)
		step.Materialized = scrubTempPaths(w, step.Materialized)
		step.MockRecorded = scrubTempPaths(w, step.MockRecorded)
		w.docCapture.Steps = append(w.docCapture.Steps, step)

		// Native channel: godog's own cucumber-message attachments, so a run
		// with a "cucumber:<file>" formatter added to Options.Format carries
		// the identical evidence as first-class step embeddings, independent
		// of this file's direct-JSON sidecar below.
		if step.CLIOutput != "" {
			c = godog.Attach(c, godog.Attachment{
				Body: []byte(step.CLIOutput), FileName: "cli-output.txt", MediaType: "text/plain",
			})
		}
		if step.MockRecorded != "" {
			c = godog.Attach(c, godog.Attachment{
				Body: []byte(step.MockRecorded), FileName: "mock-recorded.txt", MediaType: "text/plain",
			})
		}
		if step.Materialized != "" {
			c = godog.Attach(c, godog.Attachment{
				Body: []byte(step.Materialized), FileName: "materialized.txt", MediaType: "text/plain",
			})
		}
		return c, nil
	})

	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.docCapture == nil {
			return c, nil
		}
		if err != nil {
			w.docCapture.Steps = append(w.docCapture.Steps, docCaptureStep{Text: "(scenario error)", Status: err.Error()})
		}
		if werr := flushDocCapture(dir, w.docFileName, w.docCapture); werr != nil {
			return c, werr
		}
		return c, nil
	})
}

// flushDocCapture writes one scenario's accumulated capture to
// <dir>/<file>.json. dir is created if missing.
func flushDocCapture(dir, file string, cap *docCapture) error {
	// A capture with zero steps is not a legitimate empty result —
	// the After-hook wrapper above appends exactly one docCaptureStep per
	// step actually executed (plus a synthetic one on a mid-scenario error),
	// so reaching here with none means the Before hook fired but nothing
	// downstream of it ever ran a step at all. Writing a syntactically valid
	// JSON file for that would let assertAllPassed/assertAllHaveEvidence
	// (scripts/gendocs/livingdocs/render.go) both pass VACUOUSLY over an
	// empty Steps slice and generate a page proving nothing happened.
	if len(cap.Steps) == 0 {
		return fmt.Errorf("doc capture for scenario %q (%s) has zero steps -- refusing to write a capture that would let the generator's honesty checks pass vacuously", cap.Scenario, cap.Feature)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, file), data, 0644)
}
