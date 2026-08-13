//go:build acceptance

// P3, the hook-FIRING probe (capability_hook_firing.feature): the steps that
// build one cell's fixture, buy its turn, and read back the stamp file the
// hook was supposed to write.
//
// THE FIXTURE MATERIAL AND THE VERDICT ARE NOT HERE. They live in
// capability_hook_probe.go, which is UNTAGGED so `just test` compiles and runs
// them without a built binary, a live engine or a paid turn. Only the godog
// wiring — which needs the acceptance World, testenv and the live-engine gates
// — is in this file. Read capability_hook_probe.go's header for what the probe
// claims and why the assertion is a file rather than a sentence.
//
// WHAT THIS FILE ADDS TO THAT: the ORDER OF OPERATIONS, which is where a
// hook-firing probe can most easily lie to itself.
//
//  1. The stamp file must NOT exist before the run. It is created by the hook
//     or by nothing, so a leftover file from an earlier cell — a real hazard on
//     a box that runs these one at a time, all day — would let a cell pass on
//     evidence a previous run produced. The Given step deletes it and refuses
//     to continue if it cannot.
//  2. The stamp path lives OUTSIDE the project directory the engine is launched
//     in. Inside the cwd, the agent could stumble into it (or be handed it by a
//     context surface) and the probe would be measuring reachability rather
//     than execution. A sibling of the project under the same temp root keeps
//     it absolute, private, and swept up with the rest of the scenario.
//  3. The fixture is COMMITTED, as the matrix floor commits its own, so every
//     cell of every engine runs against a byte-identical tree and a difference
//     between cells is the engine rather than the fixture's cleanliness.
//
// THE GATES AND THE CREDENTIAL ENVIRONMENT ARE REUSED, NOT RE-DERIVED.
// probeEngine, the liveAgents registry config and probeHostCredentialEnv are
// the same ones the engine matrix runs — extraction over copy, per the design's
// own instruction for wave 2. In particular the credential environment is not a
// convenience: point HOME at an isolated temp dir and every production
// credential path (which all resolve from the real host home) finds nothing, so
// the cell would fail for a reason that exists nowhere outside this harness.
package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// hookProbeRunTimeout bounds one cell's live run. Shorter than the matrix
// floor's container-sized budget because every P3 cell is a HOST cell buying
// one small turn; a cell that hangs past this is hung, not cold.
const hookProbeRunTimeout = 4 * time.Minute

func hookProbeOf(w *World) *hookProbeState {
	if w.hookProbe == nil {
		w.hookProbe = &hookProbeState{}
	}
	return w.hookProbe
}

// hookProbeFamily is this probe's name in a skip line, a failure message and the
// evidence sidecar. One constant so the three cannot disagree.
const hookProbeFamily = "hook-probe"

func registerCapabilityHookFiringSteps(ctx *godog.ScenarioContext) {
	// --- the cell's gate and fixture -----------------------------------------
	ctx.Step(`^the hook-firing probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			h := hookProbeOf(w)

			// P3 is a host/none probe by declaration, and the reason is
			// physical rather than cautious: the stamp file is an absolute HOST
			// path, so a containerized engine would write it into a filesystem
			// namespace this assertion cannot read and the cell would report a
			// hook-firing failure that is really a mount gap. If a container
			// row is ever added, it needs a mounted stamp path first — and that
			// belongs in the registry as a conscious edit, not here as a
			// silently tolerated axis.
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("hook-probe: cell [runtime=%s workspace=%s] is not declared for this probe — P3 is host/none only, because the stamp file is an absolute HOST path a container cell could not write where this assertion reads. Add the axis to the registry with a mounted stamp path before writing an Examples row for it", runtime, workspace)
			}
			h.engine, h.runtime, h.workspace = engine, runtime, workspace

			a, key, err := probeCellGate(c, w, hookProbeFamily, h.cell())
			if err != nil {
				return err
			}

			// The two mints. Stage (a)'s harp rides the hook command's argv;
			// stage (b)'s rides the hook's stdout and is minted ONLY for an
			// engine that ingests it — an unused mint would sit in the ledger
			// as a cell that never ran, which the foreign-harp scanner reads as
			// real.
			stampHarp, err := probeHarps.Mint(h.cell())
			if err != nil {
				return err
			}
			h.stampHarp = stampHarp
			if hookProbeIngestsHookStdout(engine) {
				echoHarp, err := probeHarps.Mint(h.echoCell())
				if err != nil {
					return err
				}
				h.echoHarp = echoHarp
			}

			// The stamp directory: absolute, and a SIBLING of the project
			// rather than a child of it. The engine is launched with the
			// project as its cwd and is handed context surfaces rooted there;
			// a stamp file inside that tree would be reachable by an agent
			// that went looking, and this probe's whole claim is that only
			// EXECUTION can produce the file.
			stampDir := filepath.Join(filepath.Dir(w.env.ProjectDir), "p3-hook-probe")
			if err := os.MkdirAll(stampDir, 0o755); err != nil {
				return fmt.Errorf("hook-probe: creating the out-of-cwd stamp directory %s: %w", stampDir, err)
			}
			h.scriptPath = filepath.Join(stampDir, "stamp-"+engine+".sh")
			h.stampPath = filepath.Join(stampDir, "stamp-"+engine+".txt")

			// The stamp file must not pre-exist. A leftover from an earlier
			// cell on this box would let this one pass on somebody else's
			// evidence, which is the most expensive false green a firing probe
			// can produce. Removed, then its ABSENCE confirmed — os.Remove
			// succeeding is not the same statement as the file being gone.
			if err := os.Remove(h.stampPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("hook-probe: cannot clear a pre-existing stamp file at %s: %w — a cell that ran with a stale stamp would pass on evidence it did not produce", h.stampPath, err)
			}
			if _, err := os.Stat(h.stampPath); err == nil {
				return fmt.Errorf("hook-probe: the stamp file %s still exists after removal — this cell cannot distinguish its own hook's output from what was already there", h.stampPath)
			}

			if err := os.WriteFile(h.scriptPath, []byte(hookProbeScript(h.stampPath, h.echoHarp)), 0o755); err != nil {
				return fmt.Errorf("hook-probe: writing the stamp script %s: %w", h.scriptPath, err)
			}

			// Evidence, printed unconditionally in the loud idiom: a GREEN cell
			// otherwise leaves no record of which harps it used, and the harps
			// are exactly what a human greps for in a spool or a transcript
			// later. This goes to the TEST RUNNER's stdout, which the engine
			// under test cannot read — the cell's own stdout is captured into a
			// buffer.
			fmt.Printf("MINT hook-probe %s: argv harp %q, stdout harp %q, stamp %s\n",
				h.cell(), h.stampHarp, h.echoHarp, h.stampPath)
			w.docStepMaterialized += fmt.Sprintf("\nhook-probe %s: argv harp %q, stdout harp %q\nstamp path: %s\nhook script:\n%s\n",
				h.cell(), h.stampHarp, h.echoHarp, h.stampPath, hookProbeScript(h.stampPath, h.echoHarp))

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			// The hook is declared as a BUNDLE hook on a profile the agent
			// binding selects — production's own authoring surface, the path
			// config.ResolveBundleHooks and the per-engine SettingsWriter
			// actually take. A fixture that wrote .claude/settings.json itself
			// would prove an engine execs files WE hand-made, which is not the
			// claim.
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-"+hookProbeAgent+".yaml",
				hookProbeBundleYAML(h.scriptPath, h.stampHarp)); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/"+hookProbeAgent+"-profile.yaml",
				"bundles:\n  - ctxloom:local@bundles/bundle-"+hookProbeAgent+"\n"); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", hookProbeConfigYAML(a, key, engine)); err != nil {
				return err
			}
			if err := w.env.GitCommit("hook-probe fixture: " + engine + " " + runtime + "/" + workspace); err != nil {
				return err
			}
			// The files the FIXTURE wrote, recorded so the carriage scan can
			// exclude them. The bundle YAML declares the hook command in plain
			// text, so a scan that counted it would report "ctxloom delivered
			// the hook" on a run where no writer ever executed.
			for _, rel := range []string{
				".ctxloom/content/bundles/bundle-" + hookProbeAgent + ".yaml",
				".ctxloom/profiles/" + hookProbeAgent + "-profile.yaml",
				".ctxloom/config.yaml",
			} {
				h.authored = append(h.authored, filepath.Join(w.env.ProjectDir, rel))
			}
			return nil
		})

	// --- the run ---------------------------------------------------------------
	ctx.Step(`^it runs one turn with that hook installed$`, func(c context.Context) error {
		w := worldFrom(c)
		h := hookProbeOf(w)
		if h.stampHarp == "" || h.scriptPath == "" {
			return fmt.Errorf("hook-probe: no cell fixture prepared — the Given step must run first")
		}

		// Stamped BEFORE the run so the carriage scan can tell this run's
		// delivered files from every other session's. One second of slack
		// absorbs filesystem timestamp granularity, which on some filesystems
		// is coarse enough to stamp a file a moment "before" a clock read taken
		// just before it was written.
		runStart := time.Now().Add(-time.Second)

		cmd := w.env.Command(nil, "run", "--agent", hookProbeAgent,
			"--workspace", h.workspace, "--one-shot", hookProbePrompt(h.echoHarp != ""))
		// Reused from the shared gate, not re-derived: production's credential
		// paths all resolve from the real host home, and starving them makes
		// the cell measure the harness.
		//
		// BEFORE the carriage watcher, deliberately. The watcher scans a root
		// built from realHomeDir, and this is the check that refuses when there
		// is no realHomeDir to build one from — behind it, a refused cell still
		// started a scanning goroutine over "/.ctxloom/sessions" first.
		if err := probeCellCredentialEnv(hookProbeFamily, cmd); err != nil {
			return err
		}

		// Started BEFORE the run, and this is not an optimisation: ctxloom
		// scrubs delivered settings at session teardown, so a scan afterwards
		// reports "no carriage" on a cell where carriage worked perfectly.
		// Measured 2026-08-13 — see hookProbeCarriageWatcher.
		watcher := hookProbeWatchCarriage(hookProbeCarriage{
			Needle:    h.scriptPath,
			Roots:     []string{w.env.ProjectDir, filepath.Join(realHomeDir, ".ctxloom", "sessions")},
			Authored:  h.authored,
			NotBefore: runStart,
		})
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("hook-probe: start run: %w", err)
		}
		timer := time.AfterFunc(hookProbeRunTimeout, func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		h.runErr = cmd.Wait()
		timer.Stop()

		h.stdout, h.stderr = stdout.String(), stderr.String()
		if exitErr, ok := h.runErr.(*exec.ExitError); ok {
			h.exitCode = exitErr.ExitCode()
		} else if h.runErr != nil {
			h.exitCode = -1
		}

		// Read the stamp THROUGH probeFileArtifact, which refuses an unreadable
		// file rather than handing back an empty one. That refusal is the whole
		// reason to use it here: a scan or a read that quietly treats "not
		// there" as "nothing in it" would turn the central finding of this probe
		// — the hook did not fire — into a check that passed over nothing.
		art, err := probeFileArtifact("the session_start hook's stamp file", h.stampPath)
		h.stampErr = err
		if err == nil {
			h.stampBody = art.Body
		}

		// Carriage evidence: EVIDENCE, never a gate. It splits a red into "we
		// never wrote the hook" (row 6, our bug) and "we wrote it and the
		// engine never ran it" (row 7, the vendor's behaviour).
		//
		// Two roots, because a project-only walk answers the wrong question:
		// claude is launched with --settings pointing at an EPHEMERAL
		// per-session directory under the real home, so its delivered hook is
		// nowhere near the project. The session root is bounded by runStart so
		// this does not become a walk of every session ever recorded.
		//
		// The fixture's own authored files are excluded by exact path. Without
		// that the scan reports the bundle YAML the probe wrote itself and
		// calls it delivery evidence — measured, and the reason Authored exists.
		h.carriage = watcher.Stop()

		w.docStepMaterialized = fmt.Sprintf(
			"hook-probe %s exit=%d\nargv harp=%s\nstdout harp=%s\nstamp path=%s\nstamp read err=%v\nstamp contents:\n%s\ncarriage: %s\nstdout:\n%s\nstderr:\n%s",
			h.cell(), h.exitCode, h.stampHarp, h.echoHarp, h.stampPath, h.stampErr,
			h.stampBody, hookProbeCarriageOrUnknown(h), h.stdout, h.stderr)

		// Printed unconditionally, for the GREEN case specifically. A red cell
		// carries all of this in its failure message; a green one would
		// otherwise say nothing at all, and "the hook fired" is a claim whose
		// evidence a reader should be able to see without re-running the cell.
		// It is also the line that makes an implausibly fast pass legible: a
		// live turn that took no time, exited nonzero, or produced a stamp
		// nobody can account for is visible here and invisible in a bare PASS.
		fmt.Printf("EVIDENCE hook-probe %s: exit=%d, stamp read err=%v, stamp bytes=%d, carriage=%s\n  stamp contents: %q\n  stdout: %q\n  stderr tail: %q\n",
			h.cell(), h.exitCode, h.stampErr, len(h.stampBody), hookProbeCarriageOrUnknown(h),
			h.stampBody, h.stdout, lastRunes(h.stderr, 400))
		return nil
	})

	// --- the assertion ----------------------------------------------------------
	ctx.Step(`^the hook's stamp file carries the harp it was given on its argv$`, func(c context.Context) error {
		w := worldFrom(c)
		return hookProbeAssert(hookProbeOf(w))
	})
}

// hookProbeConfigYAML renders config.yaml for one cell: the engine's OWN
// registry config (liveAgents[key].config, already carrying that engine's
// backend type and the cheap pinned model the whole @live lane shares) plus one
// agent binding. Appending to the registry's own string keeps ONE source of
// truth for which backend and model the live lane drives an engine with —
// exactly what matrixConfigYAML and probeConfigYAML do, for the same reason.
//
// permissions: bypass, deliberately. P3 is not a permission probe (that is P4)
// and an approval prompt on a headless turn would hang the cell and report as a
// timeout, which reads like an engine defect. The hook itself is unaffected by
// the tier: it is exec'd by the engine's own harness, not by a tool call the
// permission layer mediates.
// lastRunes returns the final n runes of s, prefixed with an ellipsis when it
// truncated. Used only for the evidence line: ctxloom's stderr on a live run
// carries a session banner and companion notices, and the interesting part —
// why a run failed — is at the END.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func hookProbeConfigYAML(a liveAgent, llmKey, engine string) string {
	var b strings.Builder
	b.WriteString(a.config)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - %s-profile\n    permissions: bypass\n",
		hookProbeAgent, llmKey, hookProbeAgent)
	if hookProbeNeedsProjectConfigHome(engine) {
		b.WriteString("    config_home: project\n")
	}
	return b.String()
}

// hookProbeNeedsProjectConfigHome reports whether this engine needs the binding
// to declare `config_home: project` before ctxloom will deliver its hooks AT
// ALL — and it is codex, for a reason measured rather than assumed.
//
// MEASURED 2026-08-13. codex resolves $CODEX_HOME to the user's REAL ~/.codex
// for any binding that does not declare `config_home: project` (the D2 ruling,
// registry.go's codex descriptor), and codex's own delivery refuses to write a
// host-owned home: internal/codex's deliveryHome returns homeIsHostOwned and
// writes nothing. So a default-bound codex run gets NO ctxloom hooks, no MCP
// and no prompts, and a P3 cell without this key would red on that refusal
// while claiming to have measured hook FIRING — the probe would be reporting a
// finding about the wrong subsystem entirely.
//
// With the key set, carriage was confirmed live: the hook command appeared in
// <project>/.ctxloom/state/<harp>/home/.codex/config.toml while the engine ran.
// That is what lets this probe's codex red be attributed to firing.
//
// claude and kiro need nothing here — both write cwd-keyed or ephemeral
// per-session surfaces that ctxloom delivers under a default binding, and
// claude's cell is green on exactly that path.
func hookProbeNeedsProjectConfigHome(engine string) bool {
	return engine == "codex"
}
