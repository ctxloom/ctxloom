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
	tlMCP *testenv.MCPClient       // J002500: taskloom's own MCP server (see steps_j002500_taskloom.go), eager (started explicitly, not lazily)

	lastTool     testenv.ToolResult // last tools/call envelope
	lastInner    map[string]any     // unwrapped inner result of lastTool
	lastInnerErr error              // error from lastTool.Inner(), if the envelope could not be unwrapped
	lastRes      string             // last resources/read text
	lastMime     string             // last resources/read MIME type

	remoteBare map[string]string // seeded remote name -> bare repo dir (for advancing)

	projectTree map[string]string // project-relative path -> content digest, from "I record the project tree"

	j000200Sources         map[string]*j000200Source // J000200: named source fixtures (personal/company/third-party/…)
	j000200Live            bool                      // J000200 @live: whether this scenario's real agent is available (else every step no-ops toward a clean skip)
	j000200RestartRecorded string                    // J000200: the mock's recorded input from the last "restart" (runFreshMockSession)
	j000300RecordFile      string                    // J000300: path the mock backend records its received input to
	j000300Recorded        string                    // J000300: the mock's recorded input from the discovery-session launch

	j000700s     *j000700State      // J000700: team-authoring journey state (see steps_j000700_team.go)
	j001500      *j001500State      // J001500: the corporate-signed/trust journey's fixture state (steps_j001500.go)
	j000800s     *j000800State      // J000800: the onboarding journey's fixture state (steps_j000800_onboarding.go)
	j000400      *j000400State      // J000400: the multi-engine journey's fixture state (steps_j000400.go)
	j002100      *j002100State      // J002100: the delegation/privilege journey's fixture state (steps_j002100_delegation.go)
	j001700s     *j001700State      // J001700: the incident journey's fixture state (steps_j001700.go)
	j002200      *j002200State      // J002200: the isolation-axes journey's fixture state (steps_j002200_isolation.go)
	isoMatrix    *isoMatrixState    // J002200 matrix: the per-engine config-home isolation fixture state (steps_j002200_isolation_matrix.go)
	probe        *probeCellState    // isolation probe: the live engine×axis cell's fixture state (steps_isolation_probe.go)
	j000600      *j000600State      // J000600: the Agent Skill materialization journey's fixture state (steps_j000600_doctor.go)
	j002500      *j002500State      // J002500: the taskloom tag-surface journey's fixture state (steps_j002500_taskloom.go)
	j001000      *j001000State      // J001000: cross-engine transcript capture's fixture state (steps_j001000_transcript_capture.go)
	evalTriggers *evalTriggersState // evaluate_triggers: the seeded deferred task's harp (steps_evaluate_triggers.go)
	mcpIndex     *mcpIndexState     // list_sessions: this scenario's accumulated index rows (steps_mcp_session_tools.go)
	j002400      *j002400State      // J002400: the container runtime-axis journey's fixture state (steps_j002400_container.go)
	j002600      *j002600State      // J002600: the worktree-task-store redirect journey's fixture state (steps_j002600_worktree_task_store.go)
	j002300      *j002300State      // J002300: cross-engine delegation — distinct context + real two-way bus (steps_j002300_cross_engine_delegation.go)
	j001400      *j001400State      // J001400: publishing a whole bundle tree and receiving every surface kind (steps_j001400_bundle_distribution.go)
	j001600      *j001600State      // J001600: the publisher-signing journey's fixture state (steps_j001600_signing.go)
	j001900      *j001900State      // J001900: the diagnosis-walk journey's fixture state (steps_j001900_diagnosis.go)
	j001300      *j001300State      // J001300: the close-out journey's fixture state (steps_j001300_closeout.go)
	j001200      *j001200State      // J001200: the recall/archaeologist journey's fixture state (steps_j001200_recall.go)
	j002000      *j002000State      // J002000: the engine-switch journey's fixture state (steps_j002000_engine_switch.go)
	j000500      *j000500State      // J000500: the editor/ACP journey's fixture state (steps_j000500_editor.go)
	ts           *tsState           // trust-surface matrix: fixture state (steps_trust_surface.go)
	contract     *contractState     // coordination_contract.feature: the advertised runner-terminated tool surface (steps_coordination_contract.go)

	skillSigners map[string]*testenv.TestSigner // skill.feature: cached per-name test signers (steps_skill.go), so "Trent"/"Mallory" resolve to the same key across a scenario's steps regardless of order
	// --- @doc capture sidecar (prototype; see steps_doc_capture.go) ---------
	docCapture           *docCapture // accumulated evidence for the current @doc scenario, nil otherwise
	docFileName          string      // filename this scenario's capture flushes to
	docLastMockRecorded  string      // last mock-recorded payload already attached, to avoid repeat-attaching it every step
	docLastRunCount      int         // env.RunCount() at the previous step, so a step that ran a command is attributed its output even when identical to the prior step's (a no-op step, which runs nothing, is not)
	docLastBobOutput     string      // J000700: last teammate-checkout output already attributed (separate stream from w.env)
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
	registerJ000200SetupSteps(ctx)
	registerJ000300Steps(ctx)
	registerJ000700Steps(ctx)
	registerJ001500Steps(ctx)
	registerJ000800Steps(ctx)
	registerJ000400Steps(ctx)
	registerCLIManageShapeSteps(ctx)
	registerCLISessionSteps(ctx)
	registerJ002100Steps(ctx)
	registerJ001700Steps(ctx)
	registerJ001800Steps(ctx)
	registerJ002200Steps(ctx)
	registerJ002200MatrixSteps(ctx)
	registerIsolationProbeSteps(ctx)
	registerJ000600Steps(ctx)
	registerJ002500Steps(ctx)
	registerJ001000Steps(ctx)
	registerJ001100SessionDistillSteps(ctx)
	registerContentDistillSteps(ctx)
	registerSessionHookSteps(ctx)
	registerMCPSessionToolSteps(ctx)
	registerEvaluateTriggersSteps(ctx)
	registerJ000100Steps(ctx)
	registerJ002400Steps(ctx)
	registerJ002600Steps(ctx)
	registerJ002300Steps(ctx)
	registerJ001600Steps(ctx)
	registerJ001400Steps(ctx)
	registerJ001900Steps(ctx)
	registerJ001300Steps(ctx)
	registerJ001200Steps(ctx)
	registerJ002000Steps(ctx)
	registerJ000500Steps(ctx)
	registerJ000900RecoverSteps(ctx)
	registerCLIACPSteps(ctx)
	registerCompanionConsentSteps(ctx)
	registerTrustCLISteps(ctx)
	registerTrustSurfaceSteps(ctx)
	registerTrustVocabularySteps(ctx)
	registerSkillSteps(ctx)
	registerRecoverSessionSteps(ctx)
	registerDocCaptureHooks(ctx)
}
