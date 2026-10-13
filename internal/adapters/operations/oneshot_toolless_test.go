package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Every arm a distillation can take — the distiller agent, an explicit
// label, the defaults.fast fallback — asks for a tool-less launch and names
// no posture (marauding-magnolia, decision #5): distill, MCP compaction and
// session compact all build on DistillerOneShot.
func TestDistillerOneShot_IsToolLessOnEveryArm(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg   *config.Config
		label string
	}{
		"the distiller agent":           {cfg: roleTestConfig(t, distillerAgent)},
		"an explicit label":             {cfg: roleTestConfig(t, distillerAgent), label: "fast-llm"},
		"the defaults.fast fallback arm": {cfg: roleTestConfig(t, triageAgent)},
	} {
		t.Run(name, func(t *testing.T) {
			var found report.Collector
			src := DistillerOneShot(LaunchFacts{}, nil, tc.cfg).Label(tc.label).WorkDir(t.TempDir()).source(&found)
			assert.True(t, src.ToolLess)
			assert.Empty(t, src.Permission, "a tool-less launch names no posture")
		})
	}
}

// Trigger triage and premise authoring stay at plan on every arm.
func TestTriageAndPremiseOneShots_StayPlan(t *testing.T) {
	cfg := roleTestConfig(t, triageAgent)
	for name, src := range map[string]launch.Source{
		"triage as its agent":    triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir()}, cfg).source(nil),
		"triage on a label":      triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir(), LLMLabel: "fast-llm"}, cfg).source(nil),
		"triage fallback":        triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir()}, roleTestConfig(t)).source(nil),
		"premise on a label":     OneShot(LaunchFacts{}, nil, cfg).Label("fast-llm").WorkDir(t.TempDir()).source(nil),
		"premise on the primary": OneShot(LaunchFacts{}, nil, cfg).WorkDir(t.TempDir()).source(nil),
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, src.ToolLess)
			assert.Equal(t, "plan", src.Permission)
		})
	}
}

// approverLessConfig declares opencode — an engine with no approval
// channel — as the fast label and as the distiller and triage agents' label.
func approverLessConfig(t *testing.T) *config.Config {
	t.Helper()
	return cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, nil, config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"oc": {Type: "opencode", Body: map[string]any{"model": "opencode/big-pickle"}}},
			Defaults: config.RoleDefaults{Primary: "oc", Fast: "oc"},
		},
		Agents: map[string]agents.Agent{distillerAgent: {LLM: "oc"}, triageAgent: {LLM: "oc"}},
	})
}

// THE POINT OF THE RULING: a distillation runs on an approver-less engine.
// Asked at plan it tripped the plan/approver gates; tool-less it is not
// plan, so it resolves — with nobody to approve anything — while trigger
// triage on the same engine, still at plan, is refused exactly as before.
func TestDistillerOneShot_RunsOnAnApproverLessEngine(t *testing.T) {
	cfg := approverLessConfig(t)
	_, loader := setupContextTestFS(t)

	o, err := testOneShotWithHome(t, cfg, opPipe(cfg, loader),
		DistillerOneShot(LaunchFacts{}, nil, cfg).WorkDir(t.TempDir()).source(nil))
	require.NoError(t, err, "a tool-less distillation launches on opencode")
	assert.Equal(t, "opencode", string(o.Launch.Engine))
	assert.True(t, o.Launch.ToolLess)
	assert.Equal(t, engine.ApproverNone, o.Launch.Permission.Approver)
	assert.False(t, o.Launch.RoutesApprovals())

	_, err = testOneShotWithHome(t, cfg, opPipe(cfg, loader),
		triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir()}, cfg).source(nil))
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured, "triage stays plan, and plan on an approver-less engine is refused")
}

// testOneShotWithHome is testOneShot over a cell that also has a session
// home: opencode delivers its surfaces under one, which testOneShot's
// project-only cell lacks.
func testOneShotWithHome(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline, src launch.Source) (*OneShotSession, error) {
	t.Helper()
	deps := testLaunchDeps(t, cfg, pipe)
	prev := prepareEnvironment
	prepareEnvironment = func(_ context.Context, req launch.CellRequest, _ isolation.Spec) (isolation.Environment, error) {
		env := stubEnvAt(req.ProjectRoot, nil)
		env.placement.Paths = present.OnHost(present.Paths{ProjectRoot: present.Root{Host: req.ProjectRoot}, SessionHome: present.Root{Host: req.SessionDir}})
		return env, nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	if src.WorkDir == "" {
		src.WorkDir = t.TempDir()
	}
	_, hosts := hostsFor(deps, &stubEngine{out: "ok"})
	o, err := StartOneShot(context.Background(), deps, hosts, sessions.Seed{ProjectDir: src.WorkDir}, src)
	if err != nil {
		return nil, err
	}
	t.Cleanup(o.End)
	return o, nil
}
