//go:build acceptance

// P2 of the capability ladder (capability_mcp_round_trip.feature): does a real
// engine, handed an ARBITRARY MCP server through ctxloom's own registration
// path, actually connect to it and call its tool?
//
// WHAT IS NEW HERE, MEASURED AGAINST WHAT ALREADY EXISTS. Capability row 8 (MCP
// registration + tool round trip) is claimed by all four engines and was, at
// this base, proven only once and only narrowly: j002300's children each called
// mcp__ctxloom__agent_send through their own forwarder, on host/none. That is
// ctxloom's OWN server, auto-registered, reached over the reach-back socket the
// coordinator stands up. A server a USER declares — the thing every
// `mcp.servers:` block in every config.yaml in the wild is — has never been
// shown to reach any engine, let alone to be called.
//
// So this file drives exactly that path and nothing else: the fixture writes a
// server into the project's config.yaml `mcp.servers`, and from there the value
// is production's the whole way — ManagedConfig.MCP → the engine's own native
// surface (claude's --mcp-config scratch file in a shared cell, codex's
// config.toml [mcp_servers], kiro's .kiro/settings/mcp.json, opencode's
// opencode.json `mcp`). Nothing in this file writes an engine's file. If it did,
// the cell would prove the engine can read a file we wrote, which nobody
// doubted, instead of proving ctxloom delivers.
//
// THE CHANNEL IS THE TOOL RESULT, AND THE PROBE ENFORCES THAT STRUCTURALLY —
// which is the only reason the fixture can sit where it now does. It lives
// INSIDE the cell's workspace, so an agent that goes looking CAN read the nonce
// file; siting was never what stopped it. The server records every tools/call it
// serves and the verdict requires one, so an engine that found the harp some
// other way reds on the tool path with its output printed beside the finding.
// See probe_mcp_fixture.go for the fixture and mcpProbeAssert for the verdict —
// both untagged, both hermetically tested, because they are the two places this
// cell's honesty actually lives.
//
// PERMISSIONS ARE BYPASS ON PURPOSE. The design gives approval mediation to P5;
// a cell that had to answer a permission prompt would be measuring two
// capabilities and attributing a red to neither. Bypass also matches how a
// one-shot runs in production anyway (SafeHeadless floors it).
//
// WHY THE GATES AND THE RUN ARE COPIES OF THE FLOOR'S, NOT AN ABSTRACTION OVER
// THEM. The Given step below repeats engine-matrix's gate stack (probeEngine,
// the per-axis auth resolvers, the loud skip) and its credential-environment
// rewrite. That repetition is deliberate at this slice: five wave-2 probes were
// authored in parallel worktrees, and a shared extraction written by any one of
// them would have been a merge conflict in the others' only shared file. The
// extraction is a follow-up (see DEFERRED in this slice's report), and it should
// happen once, after the wave lands, rather than five times during it.
package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// mcpProbeAgent is the one agent binding every P2 cell configures.
const mcpProbeAgent = "nonce"

// mcpProbeRunTimeout bounds one cell, at the floor's own eight minutes.
//
// It was six, on the reasoning that every P2 cell was host/none with no image to
// pull. Measured: kiro's cell spent six minutes in a tool-validation retry loop
// and finished at 6m00.3s — inside the bound by three tenths of a second. Had it
// lost that race the cell would have reported a RUN failure, which blames the
// engine for the harness's impatience and would have buried the actual finding
// (registration and discovery both worked; invocation never happened). A cell
// that genuinely hangs still fails here — it just fails after saying so, rather
// than by being killed next to a real result.
//
// That original reasoning no longer holds anyway: a container cell DOES pay for
// an image, so the floor's eight minutes is now the operative number rather than
// a generous one, and it is why the six was not restored.
const mcpProbeRunTimeout = 8 * time.Minute

// mcpProbeFixtureDirName is the fixture server's directory, created INSIDE the
// cell's workspace.
//
// IT USED TO BE A SIBLING OF THE PROJECT, and that placement was defence in
// depth misread as the mechanism. The nonce file sits in this directory, so a
// reader concluded that an in-workspace fixture would hand the agent the answer.
// It would — and the cell would still RED, because neither verdict accepts an
// echoed nonce: mcpProbeAssert requires the fixture server's OWN call log to
// carry a tools/call. An engine that reads the nonce file and echoes it never
// makes that entry appear. This is measured, not argued: the codex P3 row of
// 2026-08-13 answered with the correct harp after grepping it out of the
// fixture's own script, and red anyway.
//
// So the siting stopped an agent STUMBLING onto the path; it never stopped a
// determined one, which has shell tools. Giving it up buys the container axis:
// the workspace is what gets bind-mounted into a container cell, so a fixture
// sited here runs in-container and writes its log where the host assertion can
// read it. Out-of-workspace siting could never do that.
const mcpProbeFixtureDirName = "p2-mcp-fixture"

// mcpProbeState is one cell's fixture and its captured result. stdout and stderr
// are kept SEPARATE and the assertion reads stdout alone, for the same reason
// the floor does: ctxloom's human-readable diagnostics go to stderr by contract.
type mcpProbeState struct {
	engine    string
	runtime   string
	workspace string
	nonce     string
	fixture   probeMCPFixture
	relDir    string
	stdout    string
	stderr    string
	exitCode  int
	runErr    error
}

func mcpProbeOf(w *World) *mcpProbeState {
	if w.mcpProbe == nil {
		w.mcpProbe = &mcpProbeState{}
	}
	return w.mcpProbe
}

// cell is this cell's identity in the ladder's vocabulary, read from the
// Examples table on both axes. Every axis this probe declares is runnable: the
// wiring landed first (the binding carries `runtime:`, the run passes
// --workspace) and the fixture is now workspace-sited, which is what a container
// cell needs to reach it. See the P2 rows in capability_probe_registry.go.
func (m *mcpProbeState) cell() probeCellID {
	return probeCellID{Probe: probeP2, Engine: m.engine, Runtime: m.runtime, Workspace: m.workspace}
}

// mcpProbePrompt is the whole task. Three things it must do at once: name the
// tool unambiguously across four engines that namespace tools differently, say
// out loud that the value cannot be guessed (so a model does not "helpfully"
// invent one rather than reach for a tool), and state the output contract as
// strictly as the floor does — the ladder's shapes only mean the same thing
// across probes if the contract is the same one.
func mcpProbePrompt() string {
	return "An MCP server named \"" + probeMCPServerName + "\" is registered for this session. It provides exactly one tool, \"" +
		probeMCPToolName + "\", which takes no arguments and returns a nonce string. Some clients present it under a " +
		"namespaced name such as mcp__" + probeMCPServerName + "__" + probeMCPToolName + "; it is the same tool. " +
		"Do this now: call that tool, read the string it returns, and report it. You MUST call the tool — the nonce " +
		"appears nowhere in this message, nowhere in your context and nowhere in this project, so it cannot be " +
		"guessed, recalled or looked up. Then output a single JSON object and nothing else, with exactly one key, " +
		"\"" + probeMCPExpectedKey + "\", whose value is the string the tool returned, copied verbatim. Rules, all " +
		"mandatory: output JSON only; no preamble; no postamble; no explanation; no apology; no markdown code " +
		"fences; no backticks; no trailing commentary. Your entire response must be exactly one line of the form " +
		"{\"" + probeMCPExpectedKey + "\":\"THE_VALUE\"}."
}

// mcpProbeBundleYAML is the agent's composed context, and it is the one place
// the nonce DELIBERATELY IS NOT.
//
// The cell keeps a profile and a bundle at all — rather than binding an agent
// with no profile — for two reasons. It makes P2's fixture shape identical to
// P0's, so a difference between the two probes is the MCP path and not the
// binding. And it gives the false-green mutation somewhere to live: planting the
// harp in THIS string is the foreign-channel attack on P2, and the cell must
// still red because the tool was never called. The fragment states the channel
// in prose instead, which is true and carries no value.
func mcpProbeBundleYAML(binaryPath, fixtureDir string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  nonce_channel:\n    content: %q\n",
		"This session's nonce is not written down anywhere. The only way to obtain it is to call the "+
			probeMCPToolName+" tool on the "+probeMCPServerName+" MCP server.") +
		probeMCPBundleBlock(binaryPath, fixtureDir)
}

// mcpProbeConfigYAML renders config.yaml for one cell: the engine's OWN registry
// config (liveAgents[key].config — one source of truth for the backend type and
// the cheap pinned model the whole @live lane shares) and one agent binding.
//
// The fixture MCP server is NOT here. It is declared in the bundle the binding's
// profile composes — see probeMCPBundleBlock, which carries why config.yaml
// stopped being an option.
func mcpProbeConfigYAML(a liveAgent, llmKey, runtime string) string {
	var b strings.Builder
	b.WriteString(a.config)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - %s-profile\n    permissions: bypass\n",
		mcpProbeAgent, llmKey, mcpProbeAgent)
	b.WriteString(runtimeBindingLine(runtime))
	return b.String()
}

// mcpProbeFamily is this probe's name in a skip line, a failure message and the
// evidence sidecar. One constant so the three cannot disagree.
const mcpProbeFamily = "probe-p2-mcp"

func registerCapabilityMCPSteps(ctx *godog.ScenarioContext) {
	// --- the cell's gate and fixture ---------------------------------------
	ctx.Step(`^the MCP round-trip probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			m := mcpProbeOf(w)
			m.engine = engine

			m.runtime, m.workspace = runtime, workspace

			a, key, err := probeCellGate(c, w, mcpProbeFamily, m.cell())
			if err != nil {
				return err
			}

			// No interpreter gate: the fixture BUILDS its server from
			// cmd/probe-mcp-server, so there is no environmental dependency left to
			// skip on. A build failure is our bug and errors loudly rather than
			// skipping — see probeMCPBuildServer.

			nonce, err := probeHarps.Mint(m.cell())
			if err != nil {
				return err
			}
			m.nonce = nonce

			// INSIDE the workspace, which is what a container cell can reach — see
			// mcpProbeFixtureDirName for why the old sibling-of-the-project siting
			// was defence in depth rather than the mechanism.
			fixtureDir := filepath.Join(w.env.ProjectDir, mcpProbeFixtureDirName)
			m.fixture, err = probeMCPWriteFixture(fixtureDir, nonce)
			if err != nil {
				return err
			}
			// The registration names the fixture by a WORKSPACE-RELATIVE path, and
			// this is the single reason one fixture shape serves host, container/none
			// and container/worktree alike. probeMCPWorkspaceRel carries the full
			// argument; it refuses a path that escapes the workspace, so a future
			// edit that moves the fixture back outside fails HERE rather than as an
			// unexplained MCP-delivery red on a container cell.
			relBinary, relDir, err := probeMCPWorkspaceRel(w.env.ProjectDir, m.fixture)
			if err != nil {
				return err
			}
			m.relDir = relDir

			// Evidence. The sidecar goes to CTXLOOM_DOC_CAPTURE_DIR, outside the
			// cell's workspace; the printed line goes to the test runner's stdout,
			// which the engine under test cannot read (the cell's own stdout is
			// captured into a buffer). Without the print, a GREEN cell would leave no
			// record of which harp it used — and the harp's second job is to be the
			// thing a human greps for across transcripts and spools.
			w.docStepMaterialized += fmt.Sprintf("\nprobe-p2-mcp %s: minted nonce harp %q, served only by %s\n",
				m.cell(), m.nonce, m.fixture.Binary)
			fmt.Printf("MINT probe-p2-mcp %s: nonce harp %q (served by %s)\n", m.cell(), m.nonce, m.fixture.Binary)

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-"+mcpProbeAgent+".yaml", mcpProbeBundleYAML(relBinary, relDir)); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/"+mcpProbeAgent+"-profile.yaml",
				"bundles:\n  - ctxloom:local@bundles/bundle-"+mcpProbeAgent+"\n"); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml",
				mcpProbeConfigYAML(a, key, m.runtime)); err != nil {
				return err
			}
			// Committed, like the floor's fixture and for the same reason: every
			// cell then runs against a byte-identical tree, so a difference between
			// cells is the engine and never the fixture's cleanliness.
			if err := w.env.GitCommit("probe-p2-mcp fixture: " + engine); err != nil {
				return err
			}

			// A LAST CHECK BEFORE PAYING FOR A TURN: the harp must not appear in any
			// surface the FIXTURE authors — the bundle, the profile, the config. That
			// is the false green this scan was written against and still catches: a
			// fixture change that started writing the nonce into composed context
			// would turn every cell green while proving nothing, and it would look
			// exactly like success.
			//
			// The fixture's OWN directory is excluded, because the nonce file living
			// there is now the design rather than a leak. That narrows the scan and
			// the narrowing is honest: the claim it defends was never "the agent
			// cannot read the value" — mcpProbeAssert's call-log requirement is what
			// defends that, and it is untouched.
			if leaked, err := mcpProbeWorkspaceCarriesNonce(w.env.ProjectDir, m.nonce, mcpProbeFixtureDirName); err != nil {
				return err
			} else if leaked != "" {
				return fmt.Errorf("probe-p2-mcp %s: the minted harp %q appears at %s, a surface the fixture itself authored. The nonce must reach the agent ONLY through the fixture MCP server's tool result; a copy in composed context is a channel this probe does not test",
					m.cell(), m.nonce, leaked)
			}
			return nil
		})

	// --- the run ------------------------------------------------------------
	ctx.Step(`^it asks the engine to call the fixture MCP tool in one turn$`, func(c context.Context) error {
		w := worldFrom(c)
		m := mcpProbeOf(w)
		if m.nonce == "" {
			return fmt.Errorf("probe-p2-mcp: no cell fixture prepared — the Given step must run first")
		}

		cmd := w.env.Command(nil, "run", "--agent", mcpProbeAgent,
			"--workspace", m.workspace, "--one-shot", mcpProbePrompt())
		// Production's own credential machinery, resolving against the REAL host
		// home — see probeHostCredentialEnv, whose doc comment carries the full
		// argument.
		if err := probeCellCredentialEnv(mcpProbeFamily, cmd); err != nil {
			return err
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("probe-p2-mcp: start run: %w", err)
		}
		timer := time.AfterFunc(mcpProbeRunTimeout, func() {
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
		return nil
	})

	// --- the assertion ------------------------------------------------------
	ctx.Step(`^the engine's output is exactly the nonce the MCP tool returned$`, func(c context.Context) error {
		w := worldFrom(c)
		m := mcpProbeOf(w)

		runDir, err := probeCellRunDir(mcpProbeFamily, w.env.ProjectDir, m.workspace)
		if err != nil {
			return err
		}
		callLog := filepath.Join(runDir, m.relDir, probeMCPLogName)
		calls, logFound, err := probeMCPCallLog(callLog)
		if err != nil {
			return err
		}
		// Evidence for the sidecar and for a human reading a red: the cell, its
		// harp, both streams, and — the part no other probe has — how far the MCP
		// conversation actually got.
		w.docStepMaterialized = fmt.Sprintf(
			"probe-p2-mcp %s nonce=%s exit=%d\nMCP call log (%s, present=%t): %s\nstdout:\n%s\nstderr:\n%s",
			m.cell(), m.nonce, m.exitCode, callLog, logFound,
			probeMCPCallLogSummary(calls), m.stdout, m.stderr)
		// Also printed UNCONDITIONALLY, and this is not belt-and-braces. The
		// sidecar above only materializes under CTXLOOM_DOC_CAPTURE_DIR, and the
		// call log itself lives in the harness's temp tree, which is deleted the
		// moment the scenario ends. Measured the hard way: kiro's
		// cell went red, the temp tree was gone before anyone could look, and the
		// only surviving question — did the server ever start? — had no answer
		// short of paying for another turn. This line is what makes the MCP
		// conversation part of the run's own record. It goes to the test runner's
		// stdout, which the engine under test cannot read.
		fmt.Printf("MCP probe-p2-mcp %s: call log present=%t, events: %s\n",
			m.cell(), logFound, probeMCPCallLogSummary(calls))

		return mcpProbeAssert(mcpProbeRun{
			Cell:     m.cell(),
			Nonce:    m.nonce,
			Run:      probeRun{Stdout: m.stdout, Stderr: m.stderr, ExitCode: m.exitCode, Err: m.runErr},
			CallLog:  calls,
			LogFound: logFound,
		})
	})
}

// mcpProbeWorkspaceCarriesNonce walks the cell's workspace looking for the
// minted harp, skipping skipDir, and returns the first path holding it.
//
// This is the probe's AUTHORING claim, made checkable: whatever else is true,
// the harp must not appear in a surface the fixture itself wrote. "The fixture
// does not do that" is a property of code somebody will edit, and a future
// fixture that helpfully logged the harp into the config, the bundle or a
// session file would make every cell pass while proving nothing — the failure
// mode this whole ladder is built against.
//
// skipDir is the fixture's own directory, and excluding it is not a weakening of
// the above: the nonce file there is the fixture's design, and the claim that
// the agent reached the value THROUGH THE TOOL is defended by mcpProbeAssert's
// call-log requirement, which no siting decision can substitute for. An EMPTY
// skipDir scans everything, which is what the hermetic tests use.
//
// It reads the tree as bytes and does not care what a file IS: a binary that
// happens to contain the harp is as much a channel as a YAML that does.
// Unreadable entries are an ERROR rather than a skip, for the reason
// probeFileArtifact gives — a scan that quietly skips what it cannot read
// reports no leak for the wrong reason.
func mcpProbeWorkspaceCarriesNonce(dir, nonce, skipDir string) (string, error) {
	if nonce == "" {
		return "", fmt.Errorf("probe-p2-mcp: workspace scan was given an empty nonce — every file contains the empty string, so this scan would report a leak everywhere or nowhere depending on nothing")
	}
	var found string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("probe-p2-mcp: scanning the cell workspace for a leaked nonce at %s: %w", path, err)
		}
		if d.IsDir() {
			// .git stores the working tree's own bytes zlib-compressed, so it
			// can hold no plaintext the tree does not already hold — scanning it
			// finds nothing and walks a lot.
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			// The fixture's own directory, matched at the workspace root only so
			// a same-named directory deeper in the tree is still scanned.
			if skipDir != "" && path == filepath.Join(dir, skipDir) {
				return fs.SkipDir
			}
			return nil
		}
		if found != "" || !d.Type().IsRegular() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("probe-p2-mcp: cannot read %s while checking the cell workspace for a leaked nonce: %w — a scan that skips what it cannot read reports no leak for the wrong reason", path, rerr)
		}
		if bytes.Contains(b, []byte(nonce)) {
			found = path
		}
		return nil
	})
	return found, err
}
