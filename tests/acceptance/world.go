//go:build acceptance

// Package acceptance is the full-stack godog suite. Each scenario asserts a
// ctxloom state change across three axes at once — on-disk files, CLI stdout/
// exit, and mock-agent MCP traffic — over the shared testenv harness.
package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// World is the per-scenario state binding all three evaluation axes. It is
// constructed fresh in a Before hook and torn down in After, so scenarios share
// nothing.
type World struct {
	env   *testenv.TestEnvironment // isolated home+project, CLI exec, file asserts
	mock  *testenv.MockLM          // deterministic LLM backend (set by fixtures)
	mcp   *testenv.MCPClient       // mock agent: JSON-RPC stdio client (lazy)
	tlMCP *testenv.MCPClient       // J25: taskloom's own MCP server (see steps_j25_taskloom.go), eager (started explicitly, not lazily)

	lastTool     testenv.ToolResult // last tools/call envelope
	lastInner    map[string]any     // unwrapped inner result of lastTool
	lastInnerErr error              // error from lastTool.Inner(), if the envelope could not be unwrapped
	lastRes      string             // last resources/read text
	lastMime     string             // last resources/read MIME type

	remoteBare map[string]string // seeded remote name -> bare repo dir (for advancing)

	projectTree map[string]string // project-relative path -> content digest, from "I record the project tree"

	j2Sources         map[string]*j2Source // J2: named source fixtures (personal/company/third-party/…)
	j2Live            bool                 // J2 @live: whether this scenario's real agent is available (else every step no-ops toward a clean skip)
	j2RestartRecorded string               // J2: the mock's recorded input from the last "restart" (runFreshMockSession)
	j3RecordFile     string               // J3: path the mock backend records its received input to
	j3Recorded       string               // J3: the mock's recorded input from the discovery-session launch

	j7s          *j7State           // J7: team-authoring journey state (see steps_j7_team.go)
	j15           *j15State           // J15: the corporate-signed/trust journey's fixture state (steps_j15.go)
	j8s          *j8State           // J8: the onboarding journey's fixture state (steps_j8_onboarding.go)
	j4           *j4State           // J4: the multi-engine journey's fixture state (steps_j4.go)
	j21           *j21State           // J21: the delegation/privilege journey's fixture state (steps_j21_delegation.go)
	j17s          *j17State           // J17: the incident journey's fixture state (steps_j17.go)
	j22           *j22State           // J22: the isolation-axes journey's fixture state (steps_j22_isolation.go)
	isoMatrix    *isoMatrixState    // J22 matrix: the per-engine config-home isolation fixture state (steps_j22_isolation_matrix.go)
	probe        *probeCellState    // isolation probe: the live engine×axis cell's fixture state (steps_isolation_probe.go)
	j6          *j6State          // J6: the Agent Skill materialization journey's fixture state (steps_j6_doctor.go)
	j25          *j25State          // J25: the taskloom tag-surface journey's fixture state (steps_j25_taskloom.go)
	j10          *j10State          // J10: cross-engine transcript capture's fixture state (steps_j10_transcript_capture.go)
	evalTriggers *evalTriggersState // evaluate_triggers: the seeded deferred task's harp (steps_evaluate_triggers.go)
	mcpIndex     *mcpIndexState     // list_sessions: this scenario's accumulated index rows (steps_mcp_session_tools.go)
	j24          *j24State          // J24: the container runtime-axis journey's fixture state (steps_j24_container.go)
	j26          *j26State          // J26: the worktree-task-store redirect journey's fixture state (steps_j26_worktree_task_store.go)
	j23          *j23State          // J23: cross-engine delegation — distinct context + real two-way bus (steps_j23_cross_engine_delegation.go)
	j14          *j14State          // J14: publishing a whole bundle tree and receiving every surface kind (steps_j14_bundle_distribution.go)
	j16          *j16State          // J16: the publisher-signing journey's fixture state (steps_j16_signing.go)
	j19          *j19State          // J19: the diagnosis-walk journey's fixture state (steps_j19_diagnosis.go)
	j13          *j13State          // J13: the close-out journey's fixture state (steps_j13_closeout.go)
	j12          *j12State          // J12: the recall/archaeologist journey's fixture state (steps_j12_recall.go)
	j20          *j20State          // J20: the engine-switch journey's fixture state (steps_j20_engine_switch.go)
	j5          *j5State          // J5: the editor/ACP journey's fixture state (steps_j5_editor.go)
	ts           *tsState           // trust-surface matrix: fixture state (steps_trust_surface.go)
	contract     *contractState     // coordination_contract.feature: the advertised runner-terminated tool surface (steps_coordination_contract.go)

	skillSigners map[string]*testenv.TestSigner // skill.feature: cached per-name test signers (steps_skill.go), so "Trent"/"Mallory" resolve to the same key across a scenario's steps regardless of order
	// --- @doc capture sidecar (prototype; see steps_doc_capture.go) ---------
	docCapture           *docCapture // accumulated evidence for the current @doc scenario, nil otherwise
	docFileName          string      // filename this scenario's capture flushes to
	docLastMockRecorded  string      // last mock-recorded payload already attached, to avoid repeat-attaching it every step
	docLastRunCount      int         // env.RunCount() at the previous step, so a step that ran a command is attributed its output even when identical to the prior step's (a no-op step, which runs nothing, is not)
	docLastBobOutput     string      // J7: last teammate-checkout output already attributed (separate stream from w.env)
	toolCalls            int         // MCP tool invocations so far, so doc capture can tell "this step called a tool" from "a tool was called earlier"
	docLastToolCalls     int         // toolCalls at the previous step, mirroring docLastRunCount for the MCP channel
	docLastCommandOutput string      // most recent command's output within the current scenario, inherited by the Thens that assert about it; cleared per scenario
	docPrevStepType      string      // previous step's PickleStepType, to reconstruct And/But from a run of same-type steps for Gherkin keyword rendering
	docStepMaterialized  string      // set-and-consume: evidence a step observed that never flows through w.env (a file it read, a captured PTY session, a pre-materialize sync notice); the hook attaches it to that step and clears it
}

type worldKey struct{}

// worldFrom retrieves the per-scenario World from the step context.
func worldFrom(ctx context.Context) *World {
	w, _ := ctx.Value(worldKey{}).(*World)
	return w
}

// agent returns the mock-agent MCP client, starting and initializing it on first
// use so scenarios that never touch the agent pay nothing.
func (w *World) agent() (*testenv.MCPClient, error) {
	if w.mcp != nil {
		return w.mcp, nil
	}
	c, err := w.env.StartMCP()
	if err != nil {
		return nil, err
	}
	if err := c.Initialize(); err != nil {
		_ = c.Close()
		return nil, err
	}
	w.mcp = c
	return c, nil
}

// InitializeScenario wires the lifecycle hooks and registers every step. godog
// calls this once per scenario.
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		env, err := testenv.NewTestEnvironment()
		if err != nil {
			return ctx, err
		}
		if err := env.Setup(); err != nil {
			return ctx, err
		}
		w := &World{env: env}
		return context.WithValue(ctx, worldKey{}, w), nil
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(ctx)
		if w == nil {
			return ctx, nil
		}
		// This used to discard all three of these errors, so a
		// scenario that leaked a subprocess or failed to remove its temp root
		// reported nothing at all -- testenv.TestEnvironment.Cleanup's own doc
		// names exactly this discard as the primary, always-reproducible
		// mechanism behind an observed /tmp leak (see that type's doc).
		// Returning the first non-nil error lets godog attribute it to the
		// scenario instead of it vanishing silently.
		var firstErr error
		if cerr := w.mcp.Close(); cerr != nil && firstErr == nil {
			firstErr = fmt.Errorf("mcp client close: %w", cerr)
		}
		if cerr := w.tlMCP.Close(); cerr != nil && firstErr == nil {
			firstErr = fmt.Errorf("taskloom mcp client close: %w", cerr)
		}
		if cerr := w.env.Cleanup(); cerr != nil && firstErr == nil {
			firstErr = fmt.Errorf("env cleanup: %w", cerr)
		}
		return ctx, firstErr
	})

	registerFixtureSteps(ctx)
	registerCLISteps(ctx)
	registerFileSteps(ctx)
	registerMCPSteps(ctx)
	registerCoordinationContractSteps(ctx)
	registerLiveSteps(ctx)
	registerReviewSteps(ctx)
	registerJ2SetupSteps(ctx)
	registerJ3Steps(ctx)
	registerJ7Steps(ctx)
	registerJ15Steps(ctx)
	registerJ8Steps(ctx)
	registerJ4Steps(ctx)
	registerJ21Steps(ctx)
	registerJ17Steps(ctx)
	registerJ18Steps(ctx)
	registerJ22Steps(ctx)
	registerJ22MatrixSteps(ctx)
	registerIsolationProbeSteps(ctx)
	registerJ6Steps(ctx)
	registerJ25Steps(ctx)
	registerJ10Steps(ctx)
	registerJ11SessionDistillSteps(ctx)
	registerContentDistillSteps(ctx)
	registerSessionHookSteps(ctx)
	registerMCPSessionToolSteps(ctx)
	registerEvaluateTriggersSteps(ctx)
	registerJ1Steps(ctx)
	registerJ24Steps(ctx)
	registerJ26Steps(ctx)
	registerJ23Steps(ctx)
	registerJ16Steps(ctx)
	registerJ14Steps(ctx)
	registerJ19Steps(ctx)
	registerJ13Steps(ctx)
	registerJ12Steps(ctx)
	registerJ20Steps(ctx)
	registerJ5Steps(ctx)
	registerJ9RecoverSteps(ctx)
	registerCompanionConsentSteps(ctx)
	registerTrustCLISteps(ctx)
	registerTrustSurfaceSteps(ctx)
	registerTrustVocabularySteps(ctx)
	registerSkillSteps(ctx)
	registerRecoverSessionSteps(ctx)
	registerDocCaptureHooks(ctx)
}
