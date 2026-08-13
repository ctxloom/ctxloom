//go:build acceptance

// P1's godog wiring — the gate, the fixture and the paid run for one cell of the
// CONTEXT-APPROACH SWEEP (capability_context_approaches.feature).
//
// ONLY THE WIRING IS HERE. The config renderer, the pin checks and the verdict
// live in capability_context_approaches.go, UNTAGGED, so they run under plain
// `just test` without an engine or a paid turn. This file is the part that
// genuinely needs a build tag: it drives a real binary against a real
// subscription.
//
// THE STRUCTURE IS P0'S, ON PURPOSE. Same three steps (gate+fixture / run /
// assert), same shared gate (probeCellGate), same credential posture
// (probeHostCredentialEnv — production's own per-axis machinery resolving
// against the REAL host home, because a cell more cautious than the product is
// not a test of it), same separate stdout/stderr capture. Two differences, and
// both are the experiment:
//
//   - the agent binding carries `surfaces: {context: <approach>}`, so the run
//     pins the delivery mechanism instead of taking the engine's default;
//   - the gate additionally asks production whether this engine declares that
//     approach at all, before spending a turn to find out.
package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/cucumber/godog"
)

func approachOf(w *World) *approachState {
	if w.approach == nil {
		w.approach = &approachState{}
	}
	return w.approach
}

func registerContextApproachSteps(ctx *godog.ScenarioContext) {
	// --- the cell's gate and fixture ---------------------------------------
	ctx.Step(`^the approach sweep targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)", pinning context delivery to "([^"]*)" for cell variant "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace, approach, variant string) error {
			w := worldFrom(c)
			s := approachOf(w)
			s.engine, s.runtime, s.workspace = engine, runtime, workspace
			s.variant, s.approach = variant, approach

			// Two HARD errors before anything else, both about the feature file
			// rather than about this box, and therefore never skips: a variant
			// that disagrees with the approach it pins, and an approach the
			// engine's own ApproachTable does not declare. The second is the
			// registry's gated-out rule made enforceable — a cell for an
			// approach the engine declares absent must not exist at all.
			if err := approachVariantNamesItsApproach(variant, approach); err != nil {
				return err
			}
			if err := approachPinAcceptedByEngine(engine, approach); err != nil {
				return err
			}

			a, key, err := probeCellGate(c, w, approachFamily, s.cell())
			if err != nil {
				return err
			}

			// The mint, and the cell→harp record. The mapping goes into the
			// evidence sidecar (docStepMaterialized), written to
			// CTXLOOM_DOC_CAPTURE_DIR — OUTSIDE the cell's workspace,
			// deliberately: a sidecar written inside the project the engine can
			// read would hand the engine its own answer through a channel this
			// probe does not test.
			//
			// The VARIANT rides the ledger key, so the two claude-code/host/none
			// cells (system-prompt and hook) mint DIFFERENT harps. Without it
			// they would share one, and PX's leak scanner would then be unable
			// to tell a genuine cross-cell leak from two cells that were handed
			// the same value on purpose.
			nonce, err := probeHarps.Mint(s.cell())
			if err != nil {
				return err
			}
			s.nonce = nonce
			w.docStepMaterialized += fmt.Sprintf("\n%s %s: pinned context delivery to %q, minted nonce harp %q\n",
				approachFamily, s.cell(), s.approach, s.nonce)
			// Printed unconditionally too, in the loud idiom: the sidecar only
			// materializes under CTXLOOM_DOC_CAPTURE_DIR, and a GREEN cell
			// otherwise leaves no record of which harp it used — which is
			// exactly the case where you later want to grep a spool for it.
			// This goes to the test runner's stdout; the cell's own stdout is
			// captured into a buffer the engine cannot read.
			fmt.Printf("MINT %s %s: approach=%s nonce harp %q\n", approachFamily, s.cell(), s.approach, s.nonce)

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			// Byte-identical to P0's fixture — same bundle, same profile, same
			// binding — except for the one `surfaces:` block. That is what makes
			// a P1 red attributable to the mechanism.
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-"+matrixAgent+".yaml", matrixBundleYAML(s.nonce)); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/"+matrixAgent+"-profile.yaml",
				"bundles:\n  - ctxloom:local@bundles/bundle-"+matrixAgent+"\n"); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", approachConfigYAML(a, key, runtime, approach)); err != nil {
				return err
			}
			// COMMITTED on every axis, not only the worktree one that requires
			// it, so all of an engine's cells run against a byte-identical tree
			// and a difference between them is the approach, never the fixture's
			// cleanliness.
			if err := w.env.GitCommit(fmt.Sprintf("approach-sweep fixture: %s %s/%s context=%s", engine, runtime, workspace, approach)); err != nil {
				return err
			}
			// NOTHING is seeded here on purpose — each axis's credentials are
			// delivered by ctxloom's own production machinery, resolving against
			// the real host home the run is given (probeHostCredentialEnv).
			return nil
		})

	// --- the run ------------------------------------------------------------
	ctx.Step(`^it runs the JSON hello-world task in one turn under the pinned approach$`, func(c context.Context) error {
		w := worldFrom(c)
		s := approachOf(w)
		if s.nonce == "" {
			return fmt.Errorf("%s: no cell fixture prepared — the Given step must run first", approachFamily)
		}

		cmd := w.env.Command(nil, "run", "--agent", matrixAgent,
			"--workspace", s.workspace, "--one-shot", matrixPrompt())
		if err := probeCellCredentialEnv(approachFamily, cmd); err != nil {
			return err
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("%s: start run: %w", approachFamily, err)
		}
		timer := time.AfterFunc(matrixRunTimeout, func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		s.runErr = cmd.Wait()
		timer.Stop()

		s.stdout, s.stderr = stdout.String(), stderr.String()
		if exitErr, ok := s.runErr.(*exec.ExitError); ok {
			s.exitCode = exitErr.ExitCode()
		} else if s.runErr != nil {
			s.exitCode = -1
		}
		// stderr is EVIDENCE here, not noise: it is the only place production
		// says whether it honoured the pin, so it is recorded in full.
		w.docStepMaterialized = fmt.Sprintf("%s %s approach=%s nonce=%s exit=%d\nstdout:\n%s\nstderr:\n%s",
			approachFamily, s.cell(), s.approach, s.nonce, s.exitCode, s.stdout, s.stderr)
		return nil
	})

	// --- the assertion ------------------------------------------------------
	ctx.Step(`^the run's output is exactly the expected JSON object, delivered by the pinned approach$`, func(c context.Context) error {
		return approachAssert(approachOf(worldFrom(c)))
	})
}
