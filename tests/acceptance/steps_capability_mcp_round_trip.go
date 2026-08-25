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
// THE CHANNEL IS THE TOOL RESULT, AND THE PROBE ENFORCES THAT STRUCTURALLY.
// The minted harp lives only inside the fixture server, and the fixture server
// lives OUTSIDE the cell's workspace (a sibling of the project directory, never
// in it) so nothing the agent can list contains the value. But siting alone is
// an argument, and this suite does not accept arguments: the server records
// every tools/call it serves, and the verdict requires one. An engine that found
// the harp some other way reds on the tool path with its output printed beside
// the finding. See probe_mcp_fixture.go for the fixture and mcpProbeAssert for
// the verdict — both untagged, both hermetically tested, because they are the
// two places this cell's honesty actually lives.
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
// It was six, on the reasoning that every P2 cell is host/none and has no image
// to pull. Measured: kiro's cell spent six minutes in a tool-
// validation retry loop and finished at 6m00.3s — inside the bound by three
// tenths of a second. Had it lost that race the cell would have reported a RUN
// failure, which blames the engine for the harness's impatience and would have
// buried the actual finding (registration and discovery both worked; invocation
// never happened). A cell that genuinely hangs still fails here — it just fails
// after saying so, rather than by being killed next to a real result.
const mcpProbeRunTimeout = 8 * time.Minute

// mcpProbeFixtureDirName is the fixture server's directory, created as a SIBLING
// of the project directory. Siting is load-bearing, not cosmetic: the nonce file
// sits in this directory, so a directory inside the project would hand the agent
// the answer through a channel this probe does not test. It stays
// under the test environment's own root so the harness cleans it up.
const mcpProbeFixtureDirName = "p2-mcp-fixture"

// mcpProbeState is one cell's fixture and its captured result. stdout and stderr
// are kept SEPARATE and the assertion reads stdout alone, for the same reason
// the floor does: ctxloom's human-readable diagnostics go to stderr by contract.
type mcpProbeState struct {
	engine   string
	nonce    string
	fixture  probeMCPFixture
	stdout   string
	stderr   string
	exitCode int
	runErr   error
}

func mcpProbeOf(w *World) *mcpProbeState {
	if w.mcpProbe == nil {
		w.mcpProbe = &mcpProbeState{}
	}
	return w.mcpProbe
}

// cell is this cell's identity in the ladder's vocabulary. Runtime and workspace
// are FIXED rather than read from the Examples table because P2's runnable cells
// are host/none only — the container rows are registry-deferred, and the reason
// is NOT the old "reach-back is undesigned" one (that was a misattribution: the
// cross-container-comms gap governed the coordinator bus, never this fixture).
// The real blocker is delivery: the fixture is sited outside the workspace ON
// THE HOST, and a container cell runs the server inside the container where that
// path does not exist. See the P2 rows in capability_probe_registry.go, and task
// bats-excretion for the seam that would close it. A cell here that claimed
// another axis would be addressing something the registry says does not run.
func (m *mcpProbeState) cell() probeCellID {
	return probeCellID{Probe: probeP2, Engine: m.engine, Runtime: "host", Workspace: "none"}
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
func mcpProbeBundleYAML() string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  nonce_channel:\n    content: %q\n",
		"This session's nonce is not written down anywhere. The only way to obtain it is to call the "+
			probeMCPToolName+" tool on the "+probeMCPServerName+" MCP server.")
}

// mcpProbeConfigYAML renders config.yaml for one cell: the engine's OWN registry
// config (liveAgents[key].config — one source of truth for the backend type and
// the cheap pinned model the whole @live lane shares), one agent binding, and
// the fixture MCP server registered at top level.
func mcpProbeConfigYAML(a liveAgent, llmKey, binaryPath, fixtureDir string) string {
	var b strings.Builder
	b.WriteString(a.config)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - %s-profile\n    permissions: bypass\n",
		mcpProbeAgent, llmKey, mcpProbeAgent)
	b.WriteString(probeMCPConfigYAML(binaryPath, fixtureDir))
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

			// The axes are read from the table and CHECKED rather than ignored. P2's
			// runnable cells are host/none only — the container rows are registry-
			// deferred because MCP reach-back from inside a container is undesigned —
			// so a row that quietly claimed another axis would run this cell's
			// host/none fixture while its tags, its registry row and its evidence all
			// said something else. Refusing here makes the columns load-bearing.
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("probe-p2-mcp: this probe's runnable cells are host/none only, got runtime=%q workspace=%q. The container rows are DEFERRED in the probe registry because the fixture is sited outside the workspace ON THE HOST and a container cell runs the server inside the container, where that path does not exist (task bats-excretion). This step also hard-codes --workspace none and writes no runtime:, so a row claiming another axis would run a HOST fixture under that cell's name",
					runtime, workspace)
			}

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

			// OUTSIDE the project, checked here rather than assumed: the nonce file
			// lives in this directory, and a nonce the agent could read would
			// satisfy this probe through a channel it does not test.
			fixtureDir := filepath.Join(filepath.Dir(w.env.ProjectDir), mcpProbeFixtureDirName)
			if within, err := filepath.Rel(w.env.ProjectDir, fixtureDir); err == nil && !strings.HasPrefix(within, "..") {
				return fmt.Errorf("probe-p2-mcp: the fixture MCP server directory %s is INSIDE the cell's workspace %s — the nonce file lives there, so the agent could read the answer without ever calling the tool and the cell would false-green",
					fixtureDir, w.env.ProjectDir)
			}
			m.fixture, err = probeMCPWriteFixture(fixtureDir, nonce)
			if err != nil {
				return err
			}

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
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-"+mcpProbeAgent+".yaml", mcpProbeBundleYAML()); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/"+mcpProbeAgent+"-profile.yaml",
				"bundles:\n  - ctxloom:local@bundles/bundle-"+mcpProbeAgent+"\n"); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml",
				mcpProbeConfigYAML(a, key, m.fixture.Binary, m.fixture.Dir)); err != nil {
				return err
			}
			// Committed, like the floor's fixture and for the same reason: every
			// cell then runs against a byte-identical tree, so a difference between
			// cells is the engine and never the fixture's cleanliness.
			if err := w.env.GitCommit("probe-p2-mcp fixture: " + engine); err != nil {
				return err
			}

			// A LAST CHECK BEFORE PAYING FOR A TURN: the harp must not be anywhere in
			// the workspace. This is the probe's channel claim, asserted rather than
			// asserted-about — a fixture change that started writing the nonce into
			// the bundle, the config or the profile would otherwise turn every cell
			// green while proving nothing, and it would look exactly like success.
			if leaked, err := mcpProbeWorkspaceCarriesNonce(w.env.ProjectDir, m.nonce); err != nil {
				return err
			} else if leaked != "" {
				return fmt.Errorf("probe-p2-mcp %s: the minted harp %q appears in the cell's own workspace at %s. The nonce must exist ONLY in the fixture MCP server's tool result; a workspace copy is a channel this probe does not test, and the cell would pass without a round trip",
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
			"--workspace", "none", "--one-shot", mcpProbePrompt())
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

		calls, logFound, err := probeMCPCallLog(m.fixture.CallLog)
		if err != nil {
			return err
		}
		// Evidence for the sidecar and for a human reading a red: the cell, its
		// harp, both streams, and — the part no other probe has — how far the MCP
		// conversation actually got.
		w.docStepMaterialized = fmt.Sprintf(
			"probe-p2-mcp %s nonce=%s exit=%d\nMCP call log (%s, present=%t): %s\nstdout:\n%s\nstderr:\n%s",
			m.cell(), m.nonce, m.exitCode, m.fixture.CallLog, logFound,
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
// minted harp, and returns the first path holding it.
//
// This is the probe's channel claim, made checkable. P2 asserts the nonce is
// reachable ONLY through the tool result; the claim is true today because the
// fixture writes the harp into one file outside the tree, but "the fixture does
// not do that" is a property of code somebody will edit. A future fixture that
// helpfully logged the harp into the config, the bundle or a session file would
// make every cell pass while proving nothing — and it would look like success,
// which is the failure mode this whole ladder is built against.
//
// It reads the tree as bytes and does not care what a file IS: a binary that
// happens to contain the harp is as much a channel as a YAML that does.
// Unreadable entries are an ERROR rather than a skip, for the reason
// probeFileArtifact gives — a scan that quietly skips what it cannot read
// reports no leak for the wrong reason.
func mcpProbeWorkspaceCarriesNonce(dir, nonce string) (string, error) {
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
