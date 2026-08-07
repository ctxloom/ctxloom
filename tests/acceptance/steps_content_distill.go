//go:build acceptance

// Hermetic content distillation (content_distill.feature): `fragment distill`,
// `command distill` and `bundle distill` driven against the mock LLM.
//
// These three leaves were credited as covered for a long time on the strength
// of distill_live.feature alone, which is @live and never runs by default —
// coverage that existed only in the corpus reader's eyes. The credit vanished
// the moment that reader began counting scenarios that actually execute, which
// is what this file replaces it with.
//
// The assertions here deliberately do NOT judge compression quality: the mock's
// answer is canned, so "is this a faithful summary" is unanswerable and asking
// it would produce a test of the fixture. What IS answerable hermetically, and
// is what the live rows silently assume, is the wiring on both sides of the
// distiller — the item's real content going in, the distiller's real answer
// coming back out and being stored.
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// distillerAnswer asserts that stored holds what the mock actually answered,
// naming the two ways this fails apart because they have different causes.
//
// An EMPTY field is the degrade path leaking through: newDistiller returns nil
// when no label resolves, the item is stored raw, and the command still exits
// 0. A field holding the ORIGINAL is a passthrough — the distiller ran and its
// answer was dropped on the way to disk. Neither is distinguishable from
// success by exit code, which is the whole reason this step reads the file.
func distillerAnswer(w *World, kind, name, stored, original string) error {
	if w.mock == nil {
		return fmt.Errorf(`no mock LLM configured for this scenario (missing a "the mock LLM responds" step)`)
	}
	if strings.TrimSpace(stored) == "" {
		// The command's own output comes along because this failure is
		// EXPECTED to be silent — distillation warns and carries on rather
		// than failing, so whatever warning it printed is the only clue to
		// which of several degrade arms was taken.
		return fmt.Errorf("%s %q has no distilled rendering: the command exited 0 but stored the content RAW; command output:\n%s", kind, name, w.env.LastOutput())
	}
	if strings.TrimSpace(stored) == strings.TrimSpace(original) {
		return fmt.Errorf("%s %q's distilled rendering is byte-identical to its source — the distiller's answer was discarded and the original copied in its place", kind, name)
	}
	if !strings.Contains(stored, w.mock.Response) {
		return fmt.Errorf("%s %q's distilled rendering does not carry what the distiller answered (%q); stored:\n%s", kind, name, w.mock.Response, stored)
	}
	return nil
}

func registerContentDistillSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the distilled fragment "([^"]*)" in bundle "([^"]*)" is the distiller's answer$`,
		func(c context.Context, fragment, bundle string) error {
			w := worldFrom(c)
			content, distilled, err := readBundleFragment(w, bundle, fragment)
			if err != nil {
				return err
			}
			return distillerAnswer(w, "fragment", fragment, distilled, content)
		})

	ctx.Step(`^the distilled command "([^"]*)" in bundle "([^"]*)" is the distiller's answer$`,
		func(c context.Context, command, bundle string) error {
			w := worldFrom(c)
			content, distilled, err := readBundleCommand(w, bundle, command)
			if err != nil {
				return err
			}
			return distillerAnswer(w, "command", command, distilled, content)
		})

	// A backend that starts and DIES, which is what a missing engine binary or
	// an expired credential looks like from the distiller's side.
	//
	// Deliberately not "delete the llm config": with no llm section at all,
	// Config.ResolveLLM falls back to DefaultLLM for an unknown label, so a
	// distiller is still built against a phantom backend and the failure
	// arrives at spawn anyway — same observable, by a more roundabout route
	// that also depends on which backend happens to be the default. Failing
	// the backend directly says what the scenario means.
	ctx.Step(`^the distillation backend cannot start$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.mock == nil {
			return fmt.Errorf(`no mock LLM configured for this scenario (missing a "the mock LLM responds" step)`)
		}
		w.mock.ExitCode = 1
		return w.mock.WriteConfig()
	})

	// Distinct from "is the distiller's answer" because it must NOT compare
	// against the mock's CURRENT response: this asserts an EARLIER answer
	// survived a later, rejected one, so the two strings differ by design.
	ctx.Step(`^the distilled fragment "([^"]*)" in bundle "([^"]*)" still contains "([^"]*)"$`,
		func(c context.Context, fragment, bundle, want string) error {
			_, distilled, err := readBundleFragment(worldFrom(c), bundle, fragment)
			if err != nil {
				return err
			}
			if !strings.Contains(distilled, want) {
				return fmt.Errorf("fragment %q no longer carries the earlier distillation %q — the rejected answer overwrote it; stored:\n%s", fragment, want, distilled)
			}
			return nil
		})

	// The degrade path's payload claim, and the reason the warning assertion
	// beside it is not enough on its own: a run that warned AND wrote a
	// distilled rendering anyway would satisfy the warning check while
	// contradicting what the warning says happened.
	ctx.Step(`^the fragment "([^"]*)" in bundle "([^"]*)" has no distilled rendering$`,
		func(c context.Context, fragment, bundle string) error {
			w := worldFrom(c)
			content, distilled, err := readBundleFragment(w, bundle, fragment)
			if err != nil {
				return err
			}
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("fragment %q has no source content, so finding no distilled rendering proves nothing", fragment)
			}
			if strings.TrimSpace(distilled) != "" {
				return fmt.Errorf("fragment %q was stored WITH a distilled rendering despite the raw-storage warning:\n%s", fragment, distilled)
			}
			return nil
		})
}
