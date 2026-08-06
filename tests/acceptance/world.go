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
	tlMCP *testenv.MCPClient       // J11: taskloom's own MCP server (see steps_j11_taskloom.go), eager (started explicitly, not lazily)

	lastTool     testenv.ToolResult // last tools/call envelope
	lastInner    map[string]any     // unwrapped inner result of lastTool
	lastInnerErr error              // error from lastTool.Inner(), if the envelope could not be unwrapped
	lastRes      string             // last resources/read text
	lastMime     string             // last resources/read MIME type

	remoteBare map[string]string // seeded remote name -> bare repo dir (for advancing)

	projectTree map[string]string // project-relative path -> content digest, from "I record the project tree"

	j1Sources         map[string]*j1Source // J1: named source fixtures (personal/company/third-party/…)
	j1Live            bool                 // J1 @live: whether this scenario's real agent is available (else every step no-ops toward a clean skip)
	j1RestartRecorded string               // J1: the mock's recorded input from the last "restart" (runFreshMockSession)
	j1bRecordFile     string               // J1b: path the mock backend records its received input to
	j1bRecorded       string               // J1b: the mock's recorded input from the discovery-session launch

	j2s       *j2State        // J2: team-authoring journey state (see steps_j2_team.go)
	j3        *j3State        // J3: the corporate-signed/trust journey's fixture state (steps_j3.go)
	j4s       *j4State        // J4: the onboarding journey's fixture state (steps_j4_onboarding.go)
	j5        *j5State        // J5: the multi-engine journey's fixture state (steps_j5.go)
	j6        *j6State        // J6: the delegation/privilege journey's fixture state (steps_j6_delegation.go)
	j7s       *j7State        // J7: the incident journey's fixture state (steps_j7.go)
	j9        *j9State        // J9: the isolation-axes journey's fixture state (steps_j9_isolation.go)
	isoMatrix *isoMatrixState // J9 matrix: the per-engine config-home isolation fixture state (steps_j9_isolation_matrix.go)
	probe     *probeCellState // isolation probe: the live engine×axis cell's fixture state (steps_isolation_probe.go)
	j10       *j10State       // J10: the Agent Skill materialization journey's fixture state (steps_j10_doctor.go)
	j11       *j11State       // J11: the taskloom tag-surface journey's fixture state (steps_j11_taskloom.go)
	j12       *j12State       // J12: cross-engine transcript capture's fixture state (steps_j12_transcript_capture.go)
	j16       *j16State       // J16: the worktree-task-store redirect journey's fixture state (steps_j16_worktree_task_store.go)
	j17       *j17State       // J17: cross-engine delegation — distinct context + real two-way bus (steps_j17_cross_engine_delegation.go)
	j20       *j20State       // J20: publishing a whole bundle tree and receiving every surface kind (steps_j20_bundle_distribution.go)
	j18       *j18State       // J18: the publisher-signing journey's fixture state (steps_j18_signing.go)
	j21       *j21State       // J21: the diagnosis-walk journey's fixture state (steps_j21_diagnosis.go)
	j22       *j22State       // J22: the close-out journey's fixture state (steps_j22_closeout.go)
	j23       *j23State       // J23: the recall/archaeologist journey's fixture state (steps_j23_recall.go)
	j24       *j24State       // J24: the engine-switch journey's fixture state (steps_j24_engine_switch.go)
	j25       *j25State       // J25: the editor/ACP journey's fixture state (steps_j25_editor.go)
	ts        *tsState        // trust-surface matrix: fixture state (steps_trust_surface.go)
	contract  *contractState  // coordination_contract.feature: the advertised runner-terminated tool surface (steps_coordination_contract.go)

	skillSigners map[string]*testenv.TestSigner // skill.feature: cached per-name test signers (steps_skill.go), so "Trent"/"Mallory" resolve to the same key across a scenario's steps regardless of order
	// --- @doc capture sidecar (prototype; see steps_doc_capture.go) ---------
	docCapture          *docCapture // accumulated evidence for the current @doc scenario, nil otherwise
	docFileName         string      // filename this scenario's capture flushes to
	docLastMockRecorded string      // last mock-recorded payload already attached, to avoid repeat-attaching it every step
	docLastRunCount     int         // env.RunCount() at the previous step, so a step that ran a command is attributed its output even when identical to the prior step's (a no-op step, which runs nothing, is not)
	docLastBobOutput    string      // J2: last teammate-checkout output already attributed (separate stream from w.env)
	docPrevStepType     string      // previous step's PickleStepType, to reconstruct And/But from a run of same-type steps for Gherkin keyword rendering
	docStepMaterialized string      // set-and-consume: evidence a step observed that never flows through w.env (a file it read, a captured PTY session, a pre-materialize sync notice); the hook attaches it to that step and clears it
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
	registerJ1SetupSteps(ctx)
	registerJ1bSteps(ctx)
	registerJ2Steps(ctx)
	registerJ3Steps(ctx)
	registerJ4Steps(ctx)
	registerJ5Steps(ctx)
	registerJ6Steps(ctx)
	registerJ7Steps(ctx)
	registerJ8Steps(ctx)
	registerJ9Steps(ctx)
	registerJ9MatrixSteps(ctx)
	registerIsolationProbeSteps(ctx)
	registerJ10Steps(ctx)
	registerJ11Steps(ctx)
	registerJ12Steps(ctx)
	registerJ14SessionDistillSteps(ctx)
	registerJ16Steps(ctx)
	registerJ17Steps(ctx)
	registerJ18Steps(ctx)
	registerJ20Steps(ctx)
	registerJ21Steps(ctx)
	registerJ22Steps(ctx)
	registerJ23Steps(ctx)
	registerJ24Steps(ctx)
	registerJ25Steps(ctx)
	registerCompanionConsentSteps(ctx)
	registerTrustCLISteps(ctx)
	registerTrustSurfaceSteps(ctx)
	registerTrustVocabularySteps(ctx)
	registerSkillSteps(ctx)
	registerRecoverSessionSteps(ctx)
	registerDocCaptureHooks(ctx)
}
