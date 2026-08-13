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
// twice over. A random per-run nonce means no canned or memorised "hello world"
// answer can pass. Placing it in the agent's own composed profile context —
// rather than in the prompt text — additionally makes the cell prove that
// ctxloom's context delivery survived that isolation scheme, and lets a failure
// be ATTRIBUTED: valid JSON with the wrong/absent nonce is a context-delivery
// failure, the right nonce wrapped in prose is an output-format failure. The
// assertion below names which of the two it found rather than reporting a bare
// mismatch.
package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

// matrixNonce returns the per-run secret the engine must echo back. Random, so
// no memorised answer can satisfy a cell; obviously synthetic, so it can never
// be mistaken for credential material in a log.
func matrixNonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("CTXLOOM-HELLO-%d", time.Now().UnixNano())
	}
	return "CTXLOOM-HELLO-" + hex.EncodeToString(b)
}

// matrixExpected is the object the engine's entire stdout must parse to.
func matrixExpected(nonce string) map[string]any {
	return map[string]any{"hello": nonce}
}

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

// matrixRotatingCredentialEngines are the engines whose host credential is a
// SINGLE-USE, ROTATING OAuth refresh token: whenever any holder refreshes, the
// provider mints a replacement and invalidates the spent one. Copying such a
// file into a throwaway HOME is a one-way trip — the engine refreshes inside
// the copy, the provider consumes the host's token server-side, and the human
// is left needing a manual re-login. That is not hypothetical: it is exactly
// the 401 refresh_token_reused this repo has already been bitten by, and it is
// why the standing policy is that credentials reach a live run by API-key env
// var or by MAPPING, never by copy.
//
// The other two engines are not in this class. opencode's auth.json is a
// provider API key wrapped in JSON (documented in liveAgents' own entry) and
// does not rotate at all; kiro's credential is a subscription sqlite with no
// config-home var that could map it, so copyCreds is already its ONLY
// mechanism everywhere in this suite.
var matrixRotatingCredentialEngines = map[string]bool{"claude": true, "codex": true}

// errMatrixCredentialPolicy is returned when an axis can only be fed by copying
// a rotating credential — a cell that must be refused rather than run.
var errMatrixCredentialPolicy = errors.New("credential policy")

// matrixSeedCredentials puts this cell's credentials where the axis under test
// will actually look for them, which is NOT the same place on all three axes —
// and getting this wrong produces a red cell that looks like a product defect
// and is not one.
//
// MEASURED, and the reason this function exists at all: a host cell is fed by
// MAPPING (seedLiveCredentials points CLAUDE_CONFIG_DIR/CODEX_HOME/
// XDG_DATA_HOME at the real host directories), and that works because the
// engine subprocess inherits those vars. The worktree and container axes do
// their OWN credential seeding — worktree.go's seedCredentials and
// internal/lm/isolation/auth.go's resolveXContainerAuth — and both source from
// hostHomeDir(), i.e. $HOME, overriding the mapped var on the way through. In
// this harness $HOME is the scenario's isolated temp home, so a mapped cell
// reaches those axes with nothing to seed and aborts:
//
//	ctxloom: aborting startup: 1 fatal finding(s)
//	  - [isolation] worktree isolation for agent "...": no ANTHROPIC_API_KEY and
//	    no host claude credentials found to seed the per-agent config-home —
//	    the agent would start logged out
//
// That is the HARNESS's fault, not the product's: on a real box $HOME is real
// and the seeding finds the file. So those two axes need the credential in
// $HOME, which means a copy — and for a rotating credential a copy is exactly
// what policy forbids. The split is therefore by credential KIND, not by
// convenience:
//
//   - API key in the environment: nothing to do, on any axis.
//   - host axis: MAP (the policy-preferred path).
//   - worktree/container axis, non-rotating credential: copy into the isolated
//     HOME, the same thing isolation_probe.go does for the same reason.
//   - worktree/container axis, ROTATING credential: REFUSE, loudly. The cell is
//     gated out with the policy named, not quietly run in a way that could
//     consume the human's login. Setting that engine's API-key env var is the
//     supported way to fill the cell.
func matrixSeedCredentials(w *World, key string, a liveAgent, runtime, workspace string) error {
	if envSet(a.apiKeyEnvs) {
		return nil
	}
	if runtime != "container" && workspace != "worktree" {
		return seedLiveCredentials(key, a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv)
	}
	if matrixRotatingCredentialEngines[key] {
		return errMatrixCredentialPolicy
	}
	if a.copyCreds == nil {
		return fmt.Errorf("engine-matrix: %s has no credential copier, so this axis cannot be fed from the isolated HOME it seeds from", key)
	}
	return a.copyCreds(realHomeDir, w.env.HomeDir)
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

			m.nonce = matrixNonce()
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
			// Per-axis credential delivery — see matrixSeedCredentials for why
			// the three axes cannot share one mechanism, and why a rotating
			// credential is refused rather than copied.
			if err := matrixSeedCredentials(w, key, a, runtime, workspace); err != nil {
				if errors.Is(err, errMatrixCredentialPolicy) {
					return matrixSkip(engine, runtime, workspace,
						"this axis seeds credentials from $HOME, which in this harness is an isolated temp home, so filling it would mean COPYING "+key+
							"'s single-use rotating OAuth refresh token — refused by policy (a copy that refreshes consumes the human's login server-side). "+
							"Set "+strings.Join(a.apiKeyEnvs, " or ")+" to run this cell.")
				}
				return err
			}
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
		// cell's identity plus what actually came back, both streams.
		w.docStepMaterialized = fmt.Sprintf("engine-matrix [engine=%s runtime=%s workspace=%s] exit=%d\nstdout:\n%s\nstderr:\n%s",
			m.engine, m.runtime, m.workspace, m.exitCode, m.stdout, m.stderr)
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
//     shape failure (extra keys, wrong key).
func matrixAssert(m *matrixState) error {
	cell := fmt.Sprintf("[engine=%s runtime=%s workspace=%s]", m.engine, m.runtime, m.workspace)
	if m.runErr != nil {
		return fmt.Errorf("engine-matrix %s: the run itself failed (exit %d): %v\nstdout:\n%s\nstderr:\n%s",
			cell, m.exitCode, m.runErr, m.stdout, m.stderr)
	}
	trimmed := strings.TrimSpace(m.stdout)
	if trimmed == "" {
		return fmt.Errorf("engine-matrix %s: the run exited 0 and produced NO stdout at all — a silent no-op, not an output-format problem\nstderr:\n%s",
			cell, m.stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		return fmt.Errorf("engine-matrix %s: OUTPUT-FORMAT failure — stdout is not a bare JSON object (%v). The prompt forbids preamble, postamble and code fences; this is a finding about the engine, not a reason to loosen the assertion.\nstdout (verbatim):\n%s",
			cell, err, trimmed)
	}
	if !strings.Contains(trimmed, m.nonce) {
		return fmt.Errorf("engine-matrix %s: CONTEXT-DELIVERY failure — stdout is well-formed JSON but carries nothing of the nonce %q that was planted in the agent's own composed context, so the engine answered without ever seeing it.\nstdout:\n%s",
			cell, m.nonce, trimmed)
	}
	want := matrixExpected(m.nonce)
	if len(got) != len(want) {
		return fmt.Errorf("engine-matrix %s: SHAPE failure — expected exactly one key %q, got %d key(s).\nstdout:\n%s",
			cell, "hello", len(got), trimmed)
	}
	gotVal, ok := got["hello"]
	if !ok {
		return fmt.Errorf("engine-matrix %s: SHAPE failure — the object has no \"hello\" key.\nstdout:\n%s", cell, trimmed)
	}
	if gotVal != any(m.nonce) {
		return fmt.Errorf("engine-matrix %s: VALUE failure — \"hello\" is %#v, want %q.\nstdout:\n%s",
			cell, gotVal, m.nonce, trimmed)
	}
	return nil
}
