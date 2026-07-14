//go:build acceptance

// Package acceptance: the @doc capture sidecar.
//
// This is a PROTOTYPE seam for a proposed "living docs" pipeline (see
// docs/living-docs/PROPOSAL.md at the repo root of this worktree) — it is not
// wired into `just test-acceptance` or CI. It is inert unless
// CTXLOOM_DOC_CAPTURE_DIR is set, in which case every step of every
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
// generator (scripts/gen-doc-page in the proposal) refuses to render a page
// from any capture whose steps are not ALL "passed" — that is the "a red
// scenario cannot be documented" guarantee.
package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

// docCapture accumulates one @doc scenario's real, observed evidence across
// its steps, for both the native godog attachment channel and the direct
// per-scenario JSON sidecar this prototype's generator reads.
type docCapture struct {
	Scenario string           `json:"scenario"`
	Outline  string           `json:"outline,omitempty"` // Examples row, e.g. "engine=claude-code"
	Feature  string           `json:"feature"`           // source .feature file URI
	Tags     []string         `json:"tags"`
	Steps    []docCaptureStep `json:"steps"`
}

// docCaptureStep is one step's text, pass/fail outcome, and whatever real
// terminal evidence the harness observed became available immediately after
// that step ran.
type docCaptureStep struct {
	Text         string `json:"text"`
	Status       string `json:"status"`
	CLIOutput    string `json:"cli_output,omitempty"`
	MockRecorded string `json:"mock_recorded,omitempty"`
	// Materialized is content read straight from a teammate's checkout (J2:
	// findBobCommandFile) — the marker-bearing file that actually reached the
	// teammate, which is proof no CLI stdout carries. Rendered as its own
	// captured block.
	Materialized string `json:"materialized,omitempty"`
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
		return c, nil
	})

	ctx.StepContext().After(func(c context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.docCapture == nil {
			return c, nil
		}
		step := docCaptureStep{Text: st.Text, Status: status.String()}
		if w.env != nil {
			// Only attribute CLI output to THIS step if it's new since the last
			// step that had any — env.LastOutput() persists across no-op steps
			// (steps that run no CLI command, e.g. a scene-setting Given), so
			// without this guard a no-op step would misleadingly inherit the
			// PREVIOUS step's captured output as if it had produced it too.
			if out := w.env.LastOutput(); out != "" && out != w.docLastCLIOutput {
				step.CLIOutput = out
				w.docLastCLIOutput = out
			}
		}
		// J2 runs the teammate's (Bob's) commands in a separate checkout via its
		// own exec plumbing, so that output never touches w.env.LastOutput().
		// Surface it — and the materialized file that actually reached him — on
		// the same new-since-last-step basis so each teammate-side step shows
		// its real evidence rather than inheriting a prior step's.
		if w.j2s != nil {
			if bob := w.j2s.bobOutput; bob != "" && bob != w.docLastBobOutput {
				if step.CLIOutput == "" {
					step.CLIOutput = bob
				} else {
					step.CLIOutput = step.CLIOutput + "\n" + bob
				}
				w.docLastBobOutput = bob
			}
			if body := w.j2s.bobFileBody; body != "" && body != w.docLastBobFile {
				step.Materialized = body
				w.docLastBobFile = body
			}
		}
		// Whichever mock-recorded slot this scenario populated most recently —
		// j1_setup's restart-delivery scenario uses j1RestartRecorded, j1b's
		// discovery-interview scenarios use j1bRecorded. Attach whichever is
		// non-empty and hasn't already been attached to an earlier step (avoid
		// repeating the same payload on every subsequent step once it's set).
		if w.j1RestartRecorded != "" && w.j1RestartRecorded != w.docLastMockRecorded {
			step.MockRecorded = w.j1RestartRecorded
			w.docLastMockRecorded = w.j1RestartRecorded
		} else if w.j1bRecorded != "" && w.j1bRecorded != w.docLastMockRecorded {
			step.MockRecorded = w.j1bRecorded
			w.docLastMockRecorded = w.j1bRecorded
		}
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, file), data, 0644)
}
