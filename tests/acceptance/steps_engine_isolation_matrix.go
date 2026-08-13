//go:build acceptance

// The ENGINE × ISOLATION floor (engine_isolation_matrix.feature): the simplest
// possible live round trip — "emit exactly this JSON object, nothing else" —
// run through every engine ctxloom 0.7 drives, under every combination of the
// two isolation axes (runtime host|container × workspace none|worktree).
//
// WHY IT IS SEPARATE FROM EVERYTHING NEARBY. Three live lanes already exist and
// none of them answers this question:
//
//   - j002300_cross_engine_delegation.feature proves DELEGATION (a child
//     launched by a coordinator, reporting over the bus) and only on the host
//     axis with workspace none. It says nothing about containers or worktrees.
//   - isolation_probe.feature proves ISOLATION GUARANTEES (what a run wrote,
//     where, and whether the credential leaked) per engine × axis. It asserts
//     on the filesystem census, deliberately NOT on what the engine said.
//   - j002200/j002400 prove the axis MACHINERY against the mock — the flag
//     parses, the workdir moves, the container really launches — with no
//     vendor engine involved at all.
//
// The gap all three leave is the plainest question an operator has: with THIS
// engine, under THIS isolation scheme, does a run come back with the answer at
// all? This file is that floor, and it is deliberately the least demanding task
// that can still fail honestly.
//
// WHY THE ASSERTION IS STRICT, AND WHY THAT IS THE POINT. The engine is asked
// for one JSON object and told, explicitly, no preamble, no postamble, no
// fences. The assertion parses stdout (whitespace-trimmed, nothing else
// stripped) and requires it to equal the expected object exactly. An engine
// that wraps its answer in ```json fences, or opens with "Here is your JSON:",
// goes RED — and that red is a FINDING about that engine in that cell, not a
// test bug. Loosening the assertion to accept fences would delete the only
// signal this floor exists to produce. Anything that must be tolerated gets
// recorded as a tagged, evidenced exception, never absorbed into the matcher.
//
// THE NONCE IS PLANTED IN CONTEXT, NOT IN THE PROMPT, and that is load-bearing
// twice over. A fresh per-cell nonce means no canned or memorised "hello world"
// answer can pass. Placing it in the agent's own composed profile context —
// rather than in the prompt text — additionally makes the cell prove that
// ctxloom's context delivery survived that isolation scheme, and lets a failure
// be ATTRIBUTED: valid JSON with the wrong/absent nonce is a context-delivery
// failure, the right nonce wrapped in prose is an output-format failure. The
// assertion below names which of the two it found rather than reporting a bare
// mismatch.
//
// THE NONCE IS A FRESHLY MINTED HARP (human ruling, 2026-08-12), not a hex
// blob and emphatically not the session's own harp — see probe_assert.go's
// header for the full argument. Two properties this floor gets from the change:
// the value is greppable by a human reading a spool or a transcript by eye, and
// it doubles as a CROSS-CELL LEAK TRACER, since a harp minted for one cell
// turning up in another's output is an isolation failure one grep finds
// (assertNoForeignHarps). The mint goes through the process-wide ledger so no
// two cells can collide — a collision would make the leak scanner cry wolf.
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

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// matrixAgent is the one agent binding every cell configures. Fixed, so the
// only thing that varies across the 16 cells is the engine and the two axes.
const matrixAgent = "hello"

// matrixRunTimeout bounds one cell's live run. Generous on purpose: a cold
// container cell may pull or build before the engine even starts, and a killed
// run reports as a failure with no output, which reads like an engine defect
// rather than an impatient harness. A cell that genuinely hangs still fails —
// it just fails after saying so.
const matrixRunTimeout = 8 * time.Minute

// matrixState is one cell's fixture and its captured result. stdout and stderr
// are kept SEPARATE and the assertion reads stdout alone: ctxloom's own
// human-readable diagnostics (the session banner, companion notices) go to
// stderr by contract, so a combined capture would make every cell red for
// reasons that have nothing to do with the engine.
type matrixState struct {
	engine    string // backend type, as written in the Examples table
	runtime   string // host | container
	workspace string // none | worktree
	nonce     string
	stdout    string
	stderr    string
	exitCode  int
	runErr    error
}

func matrixOf(w *World) *matrixState {
	if w.matrix == nil {
		w.matrix = &matrixState{}
	}
	return w.matrix
}

// cell is this cell's identity in the capability ladder's own vocabulary: the
// ledger key its nonce was minted under, and the stamp every failure carries.
// The probe name is fixed — this feature IS P0 of the ladder — so the ledger
// and the assertion cannot key the same cell two different ways.
func (m *matrixState) cell() probeCellID {
	return probeCellID{Probe: probeP0, Engine: m.engine, Runtime: m.runtime, Workspace: m.workspace}
}

// matrixNonce mints this cell's nonce: a FRESH harp, unique across every cell
// in the run, recorded in the ledger so the evidence sidecar and the foreign-
// harp scanner both see it.
//
// It is not the session's own harp and not a random hex string. Both
// alternatives were live here before: the session harp is ambient (an engine
// could read it from CTXLOOM_SESSION_HARP and satisfy the check without the
// channel ever delivering), and hex was fixture-private but illegible — a human
// diffing two spools cannot hold 16 hex characters in their head, and this
// value's second job is to be the thing you grep for when a cell reds.
func matrixNonce(cell probeCellID) (string, error) {
	return probeHarps.Mint(cell)
}

// matrixExpectedKey is the ONE key the engine's entire stdout must parse to an
// object of: {"hello": "<the minted harp>"}. Named once so the prompt below and
// the assertion above cannot drift apart — a probe that asks for one key and
// asserts another is a test of nothing.
const matrixExpectedKey = "hello"

// matrixPrompt is the whole task. Every constraint is stated explicitly and
// redundantly, because this floor's job is to find out which engines honour a
// plain output contract — a vaguely worded prompt would blame the engine for
// the prompt's own slack.
func matrixPrompt() string {
	return "Output a single JSON object and nothing else. The object has exactly one key, \"hello\", " +
		"and its value is the nonce string that appears in the additional context available to you in this " +
		"session (not in this message). Rules, all mandatory: output JSON only; no preamble; no postamble; " +
		"no explanation; no apology; no markdown code fences; no backticks; no trailing commentary. Your " +
		"entire response must be exactly one line of the form {\"hello\":\"THE_NONCE\"}."
}

// matrixBundleYAML plants the nonce as the agent's own composed context.
func matrixBundleYAML(nonce string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  nonce:\n    content: %q\n",
		"The nonce for this session is "+nonce)
}

// matrixConfigYAML renders config.yaml for one cell: the engine's OWN registry
// config (live_engine_registry.go's liveAgents[key].config, already carrying
// that engine's backend type and the cheap pinned model the whole @live lane
// shares) plus one agent binding, and `runtime: container` for the container
// axis only. Appending to the registry's own string keeps ONE source of truth
// for which backend type and model the live lane drives an engine with —
// exactly what probeConfigYAML (isolation_probe.go) does for the same reason.
//
// The workspace axis is NOT written here: it rides the `--workspace` flag on
// the run, mirroring the isolation probe, so a cell exercises the same public
// surface an operator would type.
func matrixConfigYAML(a liveAgent, llmKey, runtime string) string {
	var b strings.Builder
	b.WriteString(a.config)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - %s-profile\n    permissions: bypass\n",
		matrixAgent, llmKey, matrixAgent)
	if runtime == "container" {
		b.WriteString("    runtime: container\n")
	}
	return b.String()
}

// matrixHostCredentialEnv rewrites a cell's command environment so that
// ctxloom's OWN per-axis credential machinery resolves against the REAL host
// home — because that is the mechanism under test, and starving it was making
// this matrix measure the harness instead of the product.
//
// WHAT WENT WRONG BEFORE, AND WHY THIS IS THE FIX. testenv isolates HOME to a
// temp dir, which is right for filesystem assertions and wrong here: EVERY
// production credential path resolves from hostHomeDir() — worktree.go's
// seedCredentials via credentialSeedSpecs, and the container mounts
// (claudeCredentialCopyMounts read-write, codexCredentialMounts /
// opencodeCredentialMounts read-only) all start there. Point HOME at an empty
// temp dir and every one of them finds nothing, so cells failed or had to be
// gated for reasons that exist nowhere outside this harness. Worse, the
// obvious workaround — the harness copying credentials into its fake home —
// would have made the cell MORE cautious than the product it verifies, and a
// cell that does not exercise production's credential mechanism proves nothing
// about it.
//
// So the credential mechanism is deliberately NOT isolated: HOME and the XDG
// roots point at the real ones, exactly as they do for a user typing this
// command. Everything this floor actually asserts on stays isolated — the
// project directory is still a fresh temp checkout carrying the fixture, and
// the assertion reads only the run's stdout, never HOME. The cost is stated
// plainly: a cell writes session state under the real ~/.ctxloom and lets the
// engine refresh its own credential in place, which is precisely what a real
// run does and precisely what makes claude's rotating token safe here (merge
// 07072acf: the container credential mount shares the real store read-write,
// so a refresh lands in the live chain rather than dying in a copy).
//
// The fake entries are REMOVED before the real ones are appended, never merely
// appended after: a duplicate key in a child environment is resolved by the C
// library, and glibc's getenv returns the FIRST match, so appending alone
// would silently lose to the isolated value.
func matrixHostCredentialEnv(env []string, realHome string) []string {
	shadowed := map[string]bool{
		"HOME": true, "USERPROFILE": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	}
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && shadowed[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"HOME="+realHome,
		"USERPROFILE="+realHome,
		"XDG_CONFIG_HOME="+filepath.Join(realHome, ".config"),
		"XDG_DATA_HOME="+filepath.Join(realHome, ".local", "share"),
	)
}

// matrixSkip prints the cell's own reason and skips. Never silent, and always
// naming the engine and both axes, because a matrix whose blanks have no
// reasons attached is indistinguishable from a matrix nobody ran.
func matrixSkip(engine, runtime, workspace, reason string) error {
	fmt.Printf("SKIP engine-matrix cell [engine=%s runtime=%s workspace=%s]: %s\n",
		engine, runtime, workspace, reason)
	return godog.ErrSkip
}

func registerEngineMatrixSteps(ctx *godog.ScenarioContext) {
	// --- the cell's gate and fixture ---------------------------------------
	//
	// Everything is decided BEFORE a paid turn is spent, and every refusal is
	// named. Three independent gates, in cost order: is the engine there and
	// authenticated at all; can this specific AXIS authenticate it (the
	// isolation probe's own per-axis resolvers, reused rather than
	// re-derived — kiro's container axis, for instance, can only be
	// authenticated by KIRO_API_KEY, and a cell that ignored that would burn
	// a turn to discover it); and, for container cells, is a runtime actually
	// reachable here.
	ctx.Step(`^the engine matrix targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			m := matrixOf(w)

			switch runtime {
			case "host", "container":
			default:
				return fmt.Errorf("engine-matrix: unknown runtime axis %q (want host|container)", runtime)
			}
			switch workspace {
			case "none", "worktree":
			default:
				return fmt.Errorf("engine-matrix: unknown workspace axis %q (want none|worktree)", workspace)
			}
			m.engine, m.runtime, m.workspace = engine, runtime, workspace

			key := backendTypeToLiveKey(engine)
			a, ok := liveAgents[key]
			if !ok {
				return fmt.Errorf("engine-matrix: %q (resolved key %q) is not registered in liveAgents (known: %v) — a row naming an unregistered engine would skip forever and look like coverage",
					engine, key, liveAgentOrder)
			}

			status := probeEngine(key, a, realHomeDir, resolveOptIn())
			w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
			if !status.available {
				return matrixSkip(engine, runtime, workspace, status.reason)
			}

			// Per-axis auth reality, reused from the isolation probe rather
			// than re-derived: these two functions already encode production's
			// own resolveEnvOrMountAuth / seedCredentials precedence per axis,
			// including the engines whose axis simply cannot be authenticated
			// today.
			if workspace == "worktree" {
				if path, reason := probeWorktreeAuthAvailable(engine); path == probeAuthNone {
					return matrixSkip(engine, runtime, workspace, "worktree axis cannot authenticate this engine: "+reason)
				}
			}
			if runtime == "container" {
				if path, reason := probeContainerAuthAvailable(engine); path == probeAuthNone {
					return matrixSkip(engine, runtime, workspace, "container axis cannot authenticate this engine: "+reason)
				}
				if rt, _, msg := containercell.Select(c, "the engine-matrix container cell"); !rt.Available {
					return matrixSkip(engine, runtime, workspace, "no container runtime reachable here: "+msg)
				}
			}

			// The mint, and the cell→harp record. The mapping goes into the
			// evidence sidecar (docStepMaterialized), which is written to
			// CTXLOOM_DOC_CAPTURE_DIR — OUTSIDE the cell's workspace, checked
			// deliberately: a sidecar written inside the project the engine can
			// read would hand the engine its own answer through a channel this
			// probe does not test, which is the exact false-green the minted
			// nonce exists to rule out.
			nonce, err := matrixNonce(m.cell())
			if err != nil {
				return err
			}
			m.nonce = nonce
			w.docStepMaterialized += fmt.Sprintf("\nengine-matrix %s: minted nonce harp %q\n", m.cell(), m.nonce)
			// Also printed unconditionally, in the loud-skip idiom, because the
			// sidecar above only materializes under CTXLOOM_DOC_CAPTURE_DIR — so
			// without this a GREEN cell leaves no record of which harp it used.
			// A red cell gets the harp in its failure message; a green one is
			// exactly the case where you later want to grep a transcript or a
			// spool for the value, which is the whole reason the nonce is a harp
			// and not a hex blob. This goes to the test runner's stdout, which
			// the engine under test cannot read — the cell's own stdout is
			// captured into a buffer.
			fmt.Printf("MINT engine-matrix %s: nonce harp %q\n", m.cell(), m.nonce)

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-"+matrixAgent+".yaml", matrixBundleYAML(m.nonce)); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/"+matrixAgent+"-profile.yaml",
				"bundles:\n  - ctxloom:local@bundles/bundle-"+matrixAgent+"\n"); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", matrixConfigYAML(a, key, runtime)); err != nil {
				return err
			}
			// COMMITTED, always — not only for the worktree axis that requires
			// it. A worktree run refuses to carry an uncommitted parent tree
			// ("refusing to auto-commit"), and committing on every axis keeps
			// all four cells of an engine running against a byte-identical
			// fixture, so a difference between cells is the axis and never the
			// fixture's cleanliness.
			if err := w.env.GitCommit("engine-matrix fixture: " + engine + " " + runtime + "/" + workspace); err != nil {
				return err
			}
			// NOTHING is seeded here on purpose. Each axis's credentials are
			// delivered by ctxloom's own production machinery, resolving against
			// the real host home the run is given (matrixHostCredentialEnv) —
			// which is the behaviour this cell exists to exercise.
			return nil
		})

	// --- the run ------------------------------------------------------------
	//
	// The DIRECT one-shot run path, the same public surface an operator types,
	// with the workspace axis on the flag exactly as the isolation probe drives
	// it. stdout and stderr are captured separately (see matrixState).
	ctx.Step(`^it runs the JSON hello-world task in one turn$`, func(c context.Context) error {
		w := worldFrom(c)
		m := matrixOf(w)
		if m.nonce == "" {
			return fmt.Errorf("engine-matrix: no cell fixture prepared — the Given step must run first")
		}

		cmd := w.env.Command(nil, "run", "--agent", matrixAgent,
			"--workspace", m.workspace, "--one-shot", matrixPrompt())
		// The credential mechanism is production's, resolving against the real
		// host home — see matrixHostCredentialEnv.
		if realHomeDir == "" {
			return fmt.Errorf("engine-matrix: no real HOME was captured, so this cell cannot exercise production's own credential resolution")
		}
		cmd.Env = matrixHostCredentialEnv(cmd.Env, realHomeDir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("engine-matrix: start run: %w", err)
		}
		timer := time.AfterFunc(matrixRunTimeout, func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		m.runErr = cmd.Wait()
		timer.Stop()

		m.stdout, m.stderr = stdout.String(), stderr.String()
		if exitErr, ok := m.runErr.(*exec.ExitError); ok {
			m.exitCode = exitErr.ExitCode()
		} else if m.runErr != nil {
			m.exitCode = -1
		}
		// Evidence for the @doc sidecar and for a human reading a failure: the
		// cell's identity, the harp minted for it, and what actually came back
		// on both streams. The harp is repeated here on purpose — it is what
		// makes a red cell's captured payload key straight back to its cell,
		// and what a human greps for across transcripts and spools.
		w.docStepMaterialized = fmt.Sprintf("engine-matrix %s nonce=%s exit=%d\nstdout:\n%s\nstderr:\n%s",
			m.cell(), m.nonce, m.exitCode, m.stdout, m.stderr)
		return nil
	})

	// --- the assertion ------------------------------------------------------
	ctx.Step(`^the run's output is exactly the expected JSON object$`, func(c context.Context) error {
		w := worldFrom(c)
		m := matrixOf(w)
		return matrixAssert(m)
	})
}

// matrixAssert is the whole verdict, named so it is unit-testable without a
// live engine (see steps_engine_isolation_matrix_test.go). It distinguishes the
// failure shapes rather than reporting a bare mismatch, because on this matrix
// the SHAPE is the finding:
//
//   - the run itself failed (nonzero exit) — an engine/isolation failure,
//     reported with stderr, which is where ctxloom says why;
//   - stdout was EMPTY on a zero exit — this project's characteristic silent
//     no-op, called out by name so it can never read as "some other mismatch";
//   - stdout does not parse as JSON — an output-FORMAT failure (fences, prose);
//   - it parses but the nonce is nowhere in it — a CONTEXT-DELIVERY failure,
//     which is a different bug in a different subsystem;
//   - it parses and carries the nonce but is not the exact object — a
//     shape failure (extra keys, wrong key), or a value failure.
//
// The checks themselves live in probe_assert.go and are COMPOSED here rather
// than written here. That is the whole point of the extraction: P2–P7's
// verdicts get this same taxonomy for free, so a format-red on the MCP probe
// and a format-red on this floor mean the same thing and read the same way.
// This function keeps the ORDER — did it run, did it say anything, is it the
// right form, did the channel deliver, is it the right object — because the
// order is what makes each red attributable to one subsystem.
func matrixAssert(m *matrixState) error {
	v := probeVerdict{Family: "engine-matrix", Cell: m.cell(), Channel: channelComposedContext}
	trimmed, err := v.ran(probeRun{Stdout: m.stdout, Stderr: m.stderr, ExitCode: m.exitCode, Err: m.runErr})
	if err != nil {
		return err
	}
	got, err := v.jsonObject(trimmed)
	if err != nil {
		return err
	}
	if err := v.carriesNonce(trimmed, m.nonce); err != nil {
		return err
	}
	return v.exactObject(got, trimmed, matrixExpectedKey, m.nonce)
}
