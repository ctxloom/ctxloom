//go:build acceptance

// P6 — the steer echo, the RUNNING half. The judging half (the verdict
// functions, their failure taxonomy, and the tests of those tests) is untagged
// in probe_p6_steer_echo.go so `just test` executes it without a binary, an
// engine or a paid turn; this file is only the wiring that gets a real engine
// into a position to be judged.
//
// It lives beside steps_j002300_cross_engine_delegation.go and deliberately
// REUSES that journey's steps rather than restating them: the harp-remembering
// step, the payload-draining agent_recv, the bundle/profile writer and the
// per-engine config renderer are all j002300's, hardened by that journey's own
// history (the empty-coordinator-harp defect, the runner-wiring defect, the
// codex 401 detour). The three steps below are the ones P6 genuinely adds — a
// gate that also mints and switches on the mail plane, the steer itself, and
// the two assertions.
//
// THE LOCKED SCENARIOS ARE UNTOUCHED. The design's instruction for this slice is
// explicit: add the outline BESIDE the existing proofs, never rewrite them. So
// the claude-code row here does not replace J002300-LIVE-ECHO-TOKEN; it stands
// next to it, and the two now guard the same property by different routes.
package acceptance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/config"
)

// p6State is one P6 cell's fixture: which cell it is (the ledger key its steer
// harp was minted under, and the stamp every failure carries), the harp itself,
// and the isolated home the spool assertion will read.
//
// The harp is carried here rather than re-minted at each step because
// probeHarps.Mint is idempotent per cell BUT the fixture and the assertion must
// be provably reading the same value: holding it once removes the possibility
// of a step keying the ledger slightly differently and quietly minting a second
// harp that the child was never told.
type p6State struct {
	cell  probeCellID
	harp  string
	agent string
}

func p6Of(w *World) *p6State {
	if w.p6 == nil {
		w.p6 = &p6State{agent: p6SteerAgent}
	}
	return w.p6
}

// p6SpoolHomeConfigYAML is the HOME half of a P6 cell's config: the mail-plane
// cutover, switched on for this scenario only.
//
// IT HAS TO BE THE HOME LAYER. delegation.spool_tee and
// delegation.spool_delivery are ScopeMachine (internal/config/layerscope's
// policy: a substrate whose behaviour depends on THIS box's spool directory and
// mounts, which a team cannot decide once for every clone), so a project
// config.yaml declaring them does not survive a real Load. Home is where an
// operator running the soak actually leaves them, and home is therefore where a
// cell that wants the substrate has to write them.
//
// AND THE CELL HAS TO WRITE THEM ITSELF, which is the finding worth stating
// plainly: this suite isolates HOME to a temp directory, so the operator's real
// ~/.ctxloom/config.yaml — where the soak is switched on machine-wide — cannot
// reach any scenario here. A machine-wide soak flag is invisible to the
// acceptance suite by construction. Nothing in the lane would have told anyone
// that; the first cell to assert on spool bytes finds it immediately.
func p6SpoolHomeConfigYAML() string {
	return fmt.Sprintf("version: %d\n", config.CurrentConfigVersion) + `# P6 (capability probe p6-steer-echo): the mail-plane cutover, on for this
# scenario. Machine-scoped keys, so they must be written in the HOME layer —
# a project file declaring them does not survive a real config Load.
delegation:
  spool_tee: true
  spool_delivery: true
`
}

func registerP6SteerEchoSteps(ctx *godog.ScenarioContext) {
	// --- the gate, the mint, and the mail plane -----------------------------
	//
	// Everything a row needs is written HERE, before a single paid turn is
	// spent, and an unavailable engine skips with its own reason printed by
	// name — the identical discipline steps_live.go, the isolation probe and
	// j002300's own per-engine gate all apply.
	ctx.Step(`^a real "([^"]*)" engine on runtime "([^"]*)" with workspace "([^"]*)" is available for a steer-echo child carrying wake marker "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace, marker string) error {
			w := worldFrom(c)
			p6 := p6Of(w)
			j002300 := j002300Of(w)

			// The axes come from the Examples table because the drift test reads
			// them there (featureCellKeys matches engine/runtime/workspace by
			// column NAME), and they are checked against what this fixture can
			// actually BUILD — not merely against what it can name. A row whose
			// axes this fixture does not construct would be addressable,
			// runnable, and a lie about what it ran, so it is refused here
			// rather than allowed to report a host run as a container one.
			if !p6FixtureBuilds(runtime, workspace) {
				return fmt.Errorf("p6: this fixture builds %s, but the row asks for runtime=%q workspace=%q. Adding an axis means building the fixture for it, not relabelling an existing one", p6BuildableAxes(), runtime, workspace)
			}
			if err := p6RefuseEmptyMarker(engine, marker); err != nil {
				return err
			}

			key := backendTypeToLiveKey(engine)
			a, ok := liveAgents[key]
			if !ok {
				return fmt.Errorf("p6: %q is not a known live engine (registry keys: %v) — a row naming an engine the registry cannot probe would skip forever and look like coverage", engine, liveAgentOrder)
			}
			status := probeEngine(key, a, realHomeDir, resolveOptIn())
			// Loud either way, in the suite's own one-line report shape.
			w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
			if !status.available {
				fmt.Printf("SKIP p6-steer-echo [engine=%s runtime=%s workspace=%s]: %s\n", engine, runtime, workspace, status.reason)
				return godog.ErrSkip
			}

			// THE MINT, before anything is written, so a mint failure costs no
			// turn. The ledger keys this cell exactly as the registry names it,
			// which is what lets the evidence sidecar and the foreign-harp
			// scanner both see the value.
			p6.cell = p6Cell(engine, runtime, workspace)
			harp, err := probeHarps.Mint(p6.cell)
			if err != nil {
				return err
			}
			p6.harp = harp
			p6.agent = p6SteerAgent

			// The child's fixture is j002300's, verbatim: one bundle carrying
			// one fragment whose content IS the wake marker, one profile, one
			// agent binding on the engine's own registry config. The steer harp
			// is deliberately NOT in any of it.
			spec := &j002300AgentSpec{
				Name:     p6.agent,
				Profile:  p6.agent + "-profile",
				Bundle:   "bundle-" + p6.agent,
				Fragment: "marker",
				Guidance: marker,
			}
			j002300.specs[spec.Name] = spec

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, spec); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", j002300PerEngineConfigYAML(a, key, spec, engine, runtime, workspace)); err != nil {
				return err
			}
			// The mail plane, in the layer that is allowed to carry it.
			if err := w.env.WriteHomeFile(".ctxloom/config.yaml", p6SpoolHomeConfigYAML()); err != nil {
				return err
			}
			// A WORKTREE SPAWN REFUSES A DIRTY CHECKOUT. Everything above wrote
			// bundle/profile/config files into the project moments ago, and
			// operations' delegate.go refuses to auto-commit on the fixture's
			// behalf ("refusing to auto-commit for delegated agent") because a
			// worktree checkout only ever contains committed state. Committing
			// here is what makes the worktree axis reachable at all; without it
			// the cell dies before a single turn is spent, which reads as a bus
			// failure and is not one.
			if workspace == "worktree" {
				if err := w.env.GitCommit("p6 fixture: bundle, profile and config for the steer-echo child"); err != nil {
					return err
				}
			}

			// Subscription path: MAP this engine at its real credential
			// directory, never copy — see seedLiveCredentials.
			return seedLiveCredentials(key, a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv)
		})

	// --- the steer ----------------------------------------------------------
	//
	// A GENUINE agent_send through the same callTool every other tool step
	// uses, addressed by the child's runtime-minted session harp. The recipient
	// is only knowable at runtime, so this cannot ride the generic table step —
	// the identical reason j002300's own agent_send step exists — and it
	// literally contains `calls tool "agent_send"` so completeness_test.go's
	// ranAsTool credits it as real MCP coverage rather than as prose.
	ctx.Step(`^the agent calls tool "agent_send" addressed to "([^"]*)"'s session carrying this cell's minted steer harp$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			p6 := p6Of(w)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("p6: no session harp remembered for %q — spawn it first", name)
			}
			body, err := p6SteerBody(p6.harp)
			if err != nil {
				return err
			}
			w.docStepMaterialized = fmt.Sprintf("agent_send -> %s (harp %s)\n  steer body: %s", name, harp, body)
			// kind is REQUIRED on an ordinary send. This rides the
			// sender-facing tool, so it names a sender-allowed kind:
			// the steer body is prose the child echoes back.
			return callTool(c, "agent_send", map[string]any{"to": harp, "body": body, "kind": "message"})
		})

	// --- the echo -----------------------------------------------------------
	//
	// Drains the coordinator's mailbox until the STEER HARP's bytes appear, and
	// judges the outcome with p6AssertEcho so the failure carries a shape rather
	// than a bare timeout. It does not assert on "the first message from that
	// child": two messages reach a coordinator from one child harp on a live run
	// (its own agent_send, and bridgeTurnResult's copy of its turn), and which
	// lands in which agent_recv batch is a race — a floor whose PASS depended on
	// batch ordering would be measuring the scheduler.
	//
	// Every retry is a free local mailbox poll, never a second paid model call,
	// so a generous budget costs nothing. Contains `calls tool "agent_recv"` for
	// the same completeness-gate reason as above.
	ctx.Step(`^the agent calls tool "agent_recv" repeatedly, waiting up to (\d+)s total, until "([^"]*)" echoes this cell's minted steer harp$`,
		func(c context.Context, budgetSec int, name string) error {
			w := worldFrom(c)
			p6 := p6Of(w)
			j002300 := j002300Of(w)
			childHarp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("p6: no session harp remembered for %q — spawn it first", name)
			}
			v := p6Verdict(p6.cell)
			deadline := time.Now().Add(time.Duration(budgetSec) * time.Second)
			var seen []string
			for {
				if err := callTool(c, "agent_recv", map[string]any{"wait": 12}); err != nil {
					return fmt.Errorf("p6: agent_recv transport error while waiting for %q's echo: %w", name, err)
				}
				if isErr, _ := w.lastTool.IsError(); !isErr {
					msgs, merr := j002300Messages(w)
					if merr != nil {
						return merr
					}
					for _, m := range msgs {
						if from, _ := m["from"].(string); from != childHarp {
							continue
						}
						body, _ := m["body"].(string)
						seen = append(seen, body)
					}
				}
				// ONE evaluation per pass, and the SAME verdict decides both
				// whether to stop and what to report. An earlier draft re-ran the
				// assert in the expiry branch, which had a real hole in it: the
				// echo could arrive between the two calls, and the step would then
				// fail carrying a wrapped nil ("%!w(<nil>)") instead of either
				// passing or naming a shape. The mutation run that was supposed to
				// prove the assertion bites is what surfaced it — which is the
				// argument for running mutations on the error path too, not only
				// on the happy one.
				verdict := p6AssertEcho(v, p6.harp, seen)
				if verdict == nil {
					evidence := fmt.Sprintf("agent_recv — %s echoed the steer harp %s over the bus:\n  %s",
						name, p6.harp, strings.Join(seen, "\n  ---\n  "))
					w.docStepMaterialized = evidence
					// LOUD ON GREEN, not only on red. A live cell that passes in
					// seconds is indistinguishable, in a lane log, from one that
					// found what it wanted for the wrong reason; printing the
					// bodies that satisfied it is how a reader checks the pass
					// rather than trusting it.
					fmt.Printf("NOTE %s: %s\n", v.Cell, evidence)
					return nil
				}
				if time.Now().After(deadline) {
					// The verdict, not the timeout, is the report: it names the
					// shape (silent no-op / credential failure / BUS-DELIVERY)
					// and quotes every body verbatim.
					return fmt.Errorf("p6: %ds elapsed — %w", budgetSec, verdict)
				}
			}
		})

	// --- the spool evidence -------------------------------------------------
	//
	// The soak's first behavioural proof in this suite, asserted on PAYLOAD
	// BYTES. See p6AssertSpoolEvidence for exactly what is claimed (the steer is
	// a file in the child's IN plane, carrying the harp) and what is only
	// measured and reported (the OUT plane, whose participation is a property of
	// the cutover's scope rather than of P6's claim).
	ctx.Step(`^the coordinator's steer is on disk in "([^"]*)"'s own spool, in a file carrying that harp$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			p6 := p6Of(w)
			j002300 := j002300Of(w)
			childHarp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("p6: no session harp remembered for %q", name)
			}
			root := p6SpoolRoot(w.env.HomeDir, childHarp)
			census, err := p6ReadSpoolCensus(root, p6.harp)
			if err != nil {
				return fmt.Errorf("p6: delegation.spool_delivery was switched on in this cell's HOME config, so the child's spool must exist: %w", err)
			}
			w.docStepMaterialized = census.String()
			if err := p6AssertSpoolEvidence(p6Verdict(p6.cell), census, p6.harp); err != nil {
				return err
			}
			// The soak's evidence, printed on green for the same reason the echo
			// is: the whole point of this step is that a switch being ON and the
			// substrate having WRITTEN something are different facts, and only
			// the census shows the second one.
			fmt.Printf("NOTE %s: %s\n", p6.cell, census)
			return nil
		})
}
