//go:build acceptance

// P4, THE PLAN SENTINEL (capability_plan_sentinel.feature): the godog half of
// the ladder's permission-tier rung. The decisions — what counts as a breach,
// what a control must show, why a nonzero exit is evidence rather than a verdict
// — all live in probe_p4_plan_sentinel.go, which is untagged so `just test`
// executes them without an engine. This file is the plumbing: gate the cell,
// plant the sentinel, spend the turn, hand the observation to the verdict.
//
// The split is not tidiness. Every cell here is @live and paid, so anything
// expressed in this file can only ever be checked by spending money, and a
// judgement nobody can run hermetically is a judgement nobody re-reads.
//
// WHAT ONE CELL DOES. Writes a fresh temp project holding one agent binding
// whose `permissions:` key is the posture under test and a sentinel file whose
// entire contents are this cell's freshly minted harp; commits it; runs
// `ctxloom run --agent sentinel --workspace none --one-shot <order to overwrite
// the sentinel>`; then reads the sentinel back off disk. Under plan the bytes
// must be untouched; under bypass — the positive control — the write must have
// landed.
//
// THE POSTURE RIDES PRODUCTION'S OWN SURFACE, and it is worth stating exactly
// which one, because a probe that applied its posture through a back door would
// measure the back door. `permissions: plan` on the agent binding is read by
// runState.agentPermissions, resolved by cli.resolvePermissionMode (flag > agent
// binding > llm label > project default > built-in), and carried to the runner
// on pb.RunOptions.PermissionMode, where it becomes the ExecuteRequest each
// backend's buildArgs branches on. Nothing here sets a flag the product does not
// offer, and nothing here reaches around the resolver.
//
// AND THE ONE-SHOT INTERACTION MATTERS. resolvePermissionMode floors a ONESHOT
// up to bypass when the resolved posture is not SafeHeadless — there is no human
// to answer a prompt — which is why P5's approval probe cannot use this
// invocation at all. Plan IS SafeHeadless (agent.PermissionMode.SafeHeadless),
// and all four backends declare enforcesReadOnlyPlan TRUE, so
// CollapsePlanIfUnenforced leaves it alone and the ONESHOT floor does not fire:
// plan survives into the run intact. That is the shifty-scroll ruling, and
// runState.warnPlanOneshotCancels is production announcing it — its warning on
// stderr is the cheapest confirmation, per cell, that the posture arrived.
package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// p4RunTimeout bounds one cell's live run. Generous, for the reason the matrix
// floor's own timeout is: a killed run reports as a failure with no output,
// which reads like a product defect rather than an impatient harness. It is
// especially load-bearing here — p4RunHappened treats a timeout as fatal
// precisely because a mid-flight run's filesystem cannot be trusted, so an
// over-tight deadline would not merely be slow, it would turn every slow engine
// into an unmeasurable cell.
const p4RunTimeout = 8 * time.Minute

// p4State is one cell's fixture and its captured result.
type p4State struct {
	engine    string
	runtime   string
	workspace string
	posture   p4Posture
	harp      string

	stdout   string
	stderr   string
	exitCode int
	runErr   error
	started  bool
	timedOut bool
}

func p4Of(w *World) *p4State {
	if w.p4 == nil {
		w.p4 = &p4State{}
	}
	return w.p4
}

// cell is this cell's identity in the ladder's vocabulary. The Variant is the
// posture, which is what makes the pair two addressable cells rather than one
// cell run twice — and what makes the two runs mint DIFFERENT harps, so a harp
// showing up in the wrong half of the pair is visible as a leak rather than
// invisible as a shared value.
func (p *p4State) cell() probeCellID {
	return probeCellID{Probe: probeP4, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace, Variant: string(p.posture)}
}

// p4Family is this probe's name in a skip line, a failure message and the
// evidence sidecar. One constant so the three cannot disagree.
//
// The shared skip printer stamps the cell whole, so this rung's line carries
// `variant=control` or `variant=plan` — which matters more here than anywhere
// else in the ladder: P4's two arms differ only by variant, and a skip line
// that could not tell them apart would leave a reader unable to see whether the
// pair was half-run.
const p4Family = "plan-sentinel"

func registerP4PlanSentinelSteps(ctx *godog.ScenarioContext) {
	// --- the cell's gate and fixture ---------------------------------------
	ctx.Step(`^the plan-sentinel probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)" with permissions "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace, posture string) error {
			w := worldFrom(c)
			p := p4Of(w)

			// The axes are validated even though this rung declares only
			// host/none: a typo'd axis would otherwise render a cell that runs
			// somewhere nobody intended and reports under a name nobody expects.
			if runtime != "host" {
				return fmt.Errorf("plan-sentinel: runtime axis %q — this rung is host-only. A container cell needs the sentinel to be observable from OUTSIDE the container after the run, which is a different fixture, not a different Examples row", runtime)
			}
			if workspace != "none" {
				return fmt.Errorf("plan-sentinel: workspace axis %q — this rung is workspace none only. A worktree cell would move the sentinel out from under the assertion's own path", workspace)
			}
			switch p4Posture(posture) {
			case p4Plan, p4Control:
			default:
				return fmt.Errorf("plan-sentinel: unknown posture %q (want %q or %q) — a cell whose posture no verdict arm judges would run, cost a turn and be scored by nothing", posture, p4Plan, p4Control)
			}
			p.engine, p.runtime, p.workspace, p.posture = engine, runtime, workspace, p4Posture(posture)

			a, key, err := probeCellGate(c, w, p4Family, p.cell())
			if err != nil {
				return err
			}

			// The mint. A fresh harp per cell, through the process-wide ledger so
			// the two halves of a pair can never collide, recorded in the
			// evidence sidecar (written to CTXLOOM_DOC_CAPTURE_DIR, deliberately
			// OUTSIDE the project the engine can read).
			harp, err := probeHarps.Mint(p.cell())
			if err != nil {
				return err
			}
			p.harp = harp
			// Printed unconditionally as well as recorded: the sidecar only
			// materializes under CTXLOOM_DOC_CAPTURE_DIR, and a GREEN cell would
			// otherwise leave no record of which harp it planted — which is
			// exactly the case where you later want to grep a spool for it. This
			// goes to the test runner's stdout; the cell's own stdout is
			// captured into a buffer the engine cannot read.
			fmt.Printf("MINT plan-sentinel %s: sentinel harp %q\n", p.cell(), p.harp)
			w.docStepMaterialized += fmt.Sprintf("\nplan-sentinel %s: minted sentinel harp %q\n", p.cell(), p.harp)

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", p4ConfigYAML(a, key, p.posture, p.runtime)); err != nil {
				return err
			}
			// THE PLANT. The sentinel's entire content is the harp and nothing
			// else, because the plan arm asserts exact equality: any framing
			// sentence around it would have to be replicated in the assertion,
			// and a frame the assertion tolerated is a byte the engine could
			// change unnoticed.
			if err := w.env.WriteFile(p4SentinelPath, p.harp); err != nil {
				return err
			}
			// Committed, matching the matrix floor: a byte-identical fixture
			// shape across every cell means a difference between cells is the
			// posture and never the tree's cleanliness.
			if err := w.env.GitCommit("plan-sentinel fixture: " + engine + " " + posture); err != nil {
				return err
			}
			// VERIFY THE PLANT, and fail loudly if it did not take. This is what
			// licenses the verdict's treatment of a missing sentinel as a BREACH
			// rather than as a missing artifact: after this check the file
			// demonstrably existed, with the right bytes, at the moment the
			// engine started. It also refuses this project's characteristic
			// silent no-op — a writer that returns nil having written nothing.
			return p4VerifyPlant(w, p)
		})

	// --- the run ------------------------------------------------------------
	ctx.Step(`^it orders the engine to overwrite the sentinel in one turn$`, func(c context.Context) error {
		w := worldFrom(c)
		p := p4Of(w)
		if p.harp == "" {
			return fmt.Errorf("plan-sentinel: no cell fixture prepared — the Given step must run first")
		}
		// The direct one-shot run, the same public surface an operator types.
		// The posture is NOT on the command line: it rides the agent binding in
		// config.yaml, which is the surface a project actually commits and the
		// rung of the precedence chain a user pinning `permissions: plan` is
		// relying on.
		cmd := w.env.Command(nil, "run", "--agent", p4Agent,
			"--workspace", p.workspace, "--one-shot", p4Prompt())
		// Credentials resolve against the REAL host home, exactly as the matrix
		// floor does and for the same reason: every production credential path
		// starts at hostHomeDir(), and starving it would make this cell measure
		// the harness. What this cell asserts on stays isolated — the sentinel
		// is inside the fresh temp project, never under HOME.
		if err := probeCellCredentialEnv(p4Family, cmd); err != nil {
			return err
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		if err := cmd.Start(); err != nil {
			p.runErr = err
			p.exitCode = -1
			return nil // not started; the verdict reports it, with the shape
		}
		p.started = true
		timer := time.AfterFunc(p4RunTimeout, func() {
			p.timedOut = true
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		p.runErr = cmd.Wait()
		timer.Stop()

		p.stdout, p.stderr = stdout.String(), stderr.String()
		if exitErr, ok := p.runErr.(*exec.ExitError); ok {
			p.exitCode = exitErr.ExitCode()
		} else if p.runErr != nil {
			p.exitCode = -1
		}
		w.docStepMaterialized = fmt.Sprintf("plan-sentinel %s harp=%s exit=%d\nstdout:\n%s\nstderr:\n%s",
			p.cell(), p.harp, p.exitCode, p.stdout, p.stderr)
		// Printed unconditionally, for the same reason the MINT line is: a RED
		// cell carries all of this in its failure message, and a GREEN one
		// currently leaves no record at all — yet a green plan cell is exactly
		// the one somebody will later want to interrogate ("did the posture
		// really apply, or did the engine just not bother?"). The sidecar only
		// materializes under CTXLOOM_DOC_CAPTURE_DIR, so without this line the
		// evidence for a passing cell exists nowhere.
		//
		// The warning flag is EVIDENCE AND NEVER A GATE, and the distinction is
		// deliberate: warnPlanOneshotCancels is production announcing that a
		// plan posture survived resolvePermissionMode's ONESHOT floor into this
		// run, which is the cheapest per-cell confirmation that the posture
		// arrived — but it is prose, and prose is precisely what no assertion in
		// this probe is allowed to depend on. Reading it here costs nothing and
		// commits to nothing; if the sentence is reworded, this line stops being
		// informative and not one verdict changes.
		fmt.Printf("EVIDENCE plan-sentinel %s: exit=%d stdout=%dB stderr=%dB plan-oneshot-warning=%t\n",
			p.cell(), p.exitCode, len(p.stdout), len(p.stderr), p4SawPlanOneshotWarning(p.stderr))
		return nil
	})

	// --- the assertion ------------------------------------------------------
	ctx.Step(`^the sentinel's bytes hold the "([^"]*)" verdict$`, func(c context.Context, posture string) error {
		w := worldFrom(c)
		p := p4Of(w)
		if p4Posture(posture) != p.posture {
			return fmt.Errorf("plan-sentinel %s: the Then step asks for the %q verdict but the cell was set up as %q — a cell judged by the other arm's rules reports a meaningless result",
				p.cell(), posture, p.posture)
		}

		o := p4Outcome{
			Cell:     p.cell(),
			Posture:  p.posture,
			Harp:     p.harp,
			Started:  p.started,
			TimedOut: p.timedOut,
			Run:      probeRun{Stdout: p.stdout, Stderr: p.stderr, ExitCode: p.exitCode, Err: p.runErr},
		}
		// probeFileArtifact rather than a bare ReadFile: it refuses to turn an
		// unreadable file into an empty one, which is the whole reason it exists
		// — and here that distinction decides between "the run deleted the
		// sentinel" (a breach) and "the sentinel is empty" (also a breach, but a
		// different one, and the message has to say which).
		artifact, err := probeFileArtifact("the sentinel after the run", p4SentinelAbsPath(w))
		if err != nil {
			o.SentinelErr = err
		} else {
			o.Sentinel = artifact.Body
		}

		verdict := p4Assert(o, p4Controls)
		// The control files its outcome for the plan cell beside it — PASS OR
		// FAIL, and the fail is the important half: a dead control must red the
		// plan cell that would otherwise inherit its meaning from it. Recorded
		// after the verdict and before returning, so the ordering holds however
		// godog reports the step.
		if p.posture == p4Control {
			p4Controls.Record(p.engine, verdict)
		}
		return verdict
	})
}

// p4SawPlanOneshotWarning reports whether production announced, on this run's
// stderr, that a plan posture reached a headless one-shot —
// runState.warnPlanOneshotCancels. It is printed as evidence beside every cell
// and READ BY NOTHING ELSE: no verdict in this probe branches on it, and none
// may, because it is a human-readable sentence a release is free to rewrite.
// Matched on the stable middle of the sentence rather than its whole text so a
// punctuation edit does not silently turn an informative line into a misleading
// one.
func p4SawPlanOneshotWarning(stderr string) bool {
	return strings.Contains(stderr, "plan permissions has no human to approve")
}

// p4SentinelAbsPath is where the sentinel lives on disk. The run executes with
// the project directory as its cwd (workspace axis none), so the engine's
// one-line relative path and this absolute one name the same file — stated here
// because that agreement is the only thing making the prompt actionable.
func p4SentinelAbsPath(w *World) string {
	return filepath.Join(w.env.ProjectDir, p4SentinelPath)
}

// p4VerifyPlant reads the sentinel back immediately after writing it and refuses
// to continue unless it holds exactly the minted harp.
//
// This is not defensive padding. Two of the verdict's judgements are only
// honest because of it: a sentinel that is MISSING after the run is reported as
// a deletion the run performed, and a sentinel that is EMPTY is reported as a
// truncation — both of which would be slander if the fixture had never
// successfully written the file in the first place. It is also the local guard
// against this project's characteristic failure mode, a writer that returns nil
// having written zero bytes: without a read-back, a plan cell would then compare
// an empty file against an empty file and report the posture holding.
func p4VerifyPlant(w *World, p *p4State) error {
	got, err := w.env.ReadFile(p4SentinelPath)
	if err != nil {
		return fmt.Errorf("plan-sentinel %s: the sentinel could not be read back after planting it (%w) — the cell must not spend a turn asserting about a file it never wrote", p.cell(), err)
	}
	if got != p.harp {
		return fmt.Errorf("plan-sentinel %s: the sentinel was planted with the harp %q but reads back as %q. Every verdict below compares against those bytes, so a plant that did not take would make the cell measure nothing",
			p.cell(), p.harp, got)
	}
	return nil
}
