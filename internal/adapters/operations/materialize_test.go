package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// materializeCfg is an isolated project whose `reviewer` profile carries
// MARK and whose default agent composes it on llm (an engine name; "" binds
// none).
func materializeCfg(t *testing.T, mark, llm string) *config.Config {
	t.Helper()
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "`+mark+`"
`)
	return cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"security"}},
	}, config.Fixture{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"reviewer"}, LLM: llm}}})
}

// premisedCfg is an isolated project whose `premised` profile holds an
// unconditional fragment and a premised one.
func premisedCfg(t *testing.T, withUnconditional bool) *config.Config {
	t.Helper()
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	bundlesDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(filepath.Join(bundlesDir, "premise-bundle"), 0o755))
	body := "version: 1.0.0\nfragments:\n"
	if withUnconditional {
		body += "  always-applies:\n    content: \"UNCONDITIONAL-MARKER\"\n"
	}
	body += "  only-sometimes:\n    premise: \"You are about to cut a release.\"\n    content: \"PREMISED-MARKER\"\n"
	bundletree.WriteOS(t, bundlesDir, "premise-bundle", body)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(profilesDir, "premised.yaml"), "bundles:\n  - premise-bundle\n", 0o644)
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

func materialize(t *testing.T, cfg *config.Config, req MaterializeRequest) *MaterializeResult {
	t.Helper()
	res, err := Materialize(context.Background(), engines.Registry(), cfg, req)
	require.NoError(t, err)
	return res
}

func fileIn(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

// TestMaterialize_TheDefaultsAreTheDefaultAgentsProfilesAndTheConfiguredEngines
// (tests 20, 22 and 42): no profiles is the default agent's, no engine is
// the configured ones, and a target that is neither a project nor a
// repository takes the INVOKING project's profile content.
func TestMaterialize_TheDefaultsAreTheDefaultAgentsProfilesAndTheConfiguredEngines(t *testing.T) {
	cfg := materializeCfg(t, "DEFAULT-AGENT-MARK", "mock")
	target := t.TempDir()
	res := materialize(t, cfg, MaterializeRequest{Target: target, Explicit: true})
	assert.Equal(t, []string{"reviewer"}, res.Profiles)
	require.Len(t, res.Engines, 1)
	assert.Equal(t, "mock", res.Engines[0].Engine)
	assert.Equal(t, MaterializeApplied, res.Status)
	assert.Contains(t, fileIn(t, target, mock.ContextFileName), "DEFAULT-AGENT-MARK")
	assert.NoDirExists(t, filepath.Join(target, ".ctxloom"), "the target did not become a project")
	assert.NoDirExists(t, filepath.Join(target, ".git"))
}

// TestMaterialize_ContextIsDeliveredForAnArgvEngineToo (test 24): by
// default every kind is delivered, context included, for an engine whose
// in-the-loop context rides argv.
func TestMaterialize_ContextIsDeliveredForAnArgvEngineToo(t *testing.T) {
	cfg := materializeCfg(t, "ARGV-ENGINE-MARK", "")
	target := t.TempDir()
	res := materialize(t, cfg, MaterializeRequest{Target: target, Engines: []string{"claude-code"}})
	assert.Contains(t, res.Engines[0].Wrote, "context")
	assert.Equal(t, filepath.Join(target, claude.ContextFileName), res.Engines[0].ContextFile)
	assert.Contains(t, fileIn(t, target, claude.ContextFileName), "ARGV-ENGINE-MARK")
}

// TestMaterialize_DryRunWritesNothing (tests 25 and 41): a dry run reports
// its plan and writes nothing; over a missing target it creates nothing.
func TestMaterialize_DryRunWritesNothing(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	target := filepath.Join(t.TempDir(), "not-yet")
	res := materialize(t, cfg, MaterializeRequest{Target: target, Engines: []string{"mock"}, DryRun: true})
	assert.Equal(t, MaterializePlanned, res.Status)
	assert.Contains(t, res.Engines[0].Wrote, "context")
	assert.False(t, res.Created)
	assert.NoDirExists(t, target)
}

// TestMaterialize_AMissingTargetIsCreatedAndAMissingReleaseIsNothing (test
// 41).
func TestMaterialize_AMissingTargetIsCreatedAndAMissingReleaseIsNothing(t *testing.T) {
	cfg := materializeCfg(t, "CREATED-MARK", "")
	target := filepath.Join(t.TempDir(), "a", "b")
	res := materialize(t, cfg, MaterializeRequest{Target: target, Engines: []string{"mock"}})
	assert.True(t, res.Created, "the creation is reported")
	assert.Contains(t, fileIn(t, target, mock.ContextFileName), "CREATED-MARK")

	gone := filepath.Join(t.TempDir(), "gone")
	rel := materialize(t, cfg, MaterializeRequest{Target: gone, Engines: []string{"mock"}, Release: true})
	assert.Equal(t, MaterializeNothing, rel.Status)
	assert.NoDirExists(t, gone, "a release creates nothing")
}

// TestMaterialize_AFileTargetIsRefusedTyped (test 43).
func TestMaterialize_AFileTargetIsRefusedTyped(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	file := filepath.Join(t.TempDir(), "a-file")
	testsupport.WriteFileString(t, afero.NewOsFs(), file, "x", 0o644)
	_, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Target: file, Engines: []string{"mock"}})
	var notDir TargetNotDirectoryError
	require.True(t, errors.As(err, &notDir), "got %v", err)
	assert.Equal(t, file, notDir.Path)
}

// TestMaterialize_ASymlinkedTargetIsOneOwnershipKey (test 40): a relative
// path, the absolute path and a symlink to it are one target: re-delivering
// through the link leaves one section, and releasing through the link
// releases it.
func TestMaterialize_ASymlinkedTargetIsOneOwnershipKey(t *testing.T) {
	cfg := materializeCfg(t, "LINKED-MARK", "")
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(real, link))
	resolved, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	res := materialize(t, cfg, MaterializeRequest{Target: real, Engines: []string{"mock"}})
	assert.Equal(t, resolved, res.Target)
	res = materialize(t, cfg, MaterializeRequest{Target: link, Engines: []string{"mock"}})
	assert.Equal(t, resolved, res.Target, "the link is the same key")
	t.Chdir(base)
	res = materialize(t, cfg, MaterializeRequest{Target: "real", Engines: []string{"mock"}})
	assert.Equal(t, resolved, res.Target, "a relative path is the same key")
	assert.Equal(t, 1, strings.Count(fileIn(t, real, mock.ContextFileName), "LINKED-MARK"), "one section")

	materialize(t, cfg, MaterializeRequest{Target: link, Engines: []string{"mock"}, Release: true})
	assert.NoFileExists(t, filepath.Join(real, mock.ContextFileName), "released through the link")
}

// TestMaterialize_TheGlobalScopeGuardCoversAnExplicitTarget (test 26): an
// explicit target that is an engine's user-global scope is refused, and
// --force proceeds.
func TestMaterialize_TheGlobalScopeGuardCoversAnExplicitTarget(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	_, err = Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Target: home, Engines: []string{"claude-code"}, DryRun: true})
	require.ErrorContains(t, err, "refusing")
	_, err = Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Target: home, Engines: []string{"claude-code"}, DryRun: true, Force: true})
	require.NoError(t, err)
}

// TestMaterialize_WithoutSkillsAPremisedFragmentIsInline (test 28).
func TestMaterialize_WithoutSkillsAPremisedFragmentIsInline(t *testing.T) {
	cfg := premisedCfg(t, true)
	target := t.TempDir()
	res := materialize(t, cfg, MaterializeRequest{Profiles: []string{"premised"}, Target: target, Engines: []string{"claude-code"},
		Surfaces: []SurfaceSpec{{Kind: present.Context}}})
	assert.Contains(t, fileIn(t, target, claude.ContextFileName), "PREMISED-MARKER")
	assert.Empty(t, res.Engines[0].WithheldByPremise)
	assert.NoDirExists(t, filepath.Join(target, ".claude", claude.SkillsDirName))
}

// TestMaterialize_OnlySkillsCarriesThePremisedFragmentAsASkill (test 29):
// the fragment is a skill, and the empty-context refusal does not fire.
func TestMaterialize_OnlySkillsCarriesThePremisedFragmentAsASkill(t *testing.T) {
	cfg := premisedCfg(t, false)
	target := t.TempDir()
	res := materialize(t, cfg, MaterializeRequest{Profiles: []string{"premised"}, Target: target, Engines: []string{"claude-code"},
		Surfaces: []SurfaceSpec{{Kind: present.Skills}}})
	require.Len(t, res.Engines[0].WithheldByPremise, 1)
	assert.Equal(t, "skills", res.Engines[0].WithheldByPremise[0].Delivered)
	assert.DirExists(t, filepath.Join(target, ".claude", claude.SkillsDirName))
	assert.NoFileExists(t, filepath.Join(target, claude.ContextFileName))
}

// TestMaterialize_RefusesAnIncoherentRequest (part of test 31, and test 51's
// programming-error half).
func TestMaterialize_RefusesAnIncoherentRequest(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	target := t.TempDir()
	_, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{})
	require.ErrorIs(t, err, ErrMaterializeNoTarget)
	for name, req := range map[string]MaterializeRequest{
		"release with profiles": {Release: true, Profiles: []string{"reviewer"}},
		"release with a dest":   {Release: true, Surfaces: []SurfaceSpec{{Kind: present.Context, Dest: "x.md"}}},
		"dest off context":      {Surfaces: []SurfaceSpec{{Kind: present.MCP, Dest: "x.json"}}},
		"unknown mechanism":     {Surfaces: []SurfaceSpec{{Kind: present.Context, Mechanism: "hook"}}},
		"one kind two ways":     {Surfaces: []SurfaceSpec{{Kind: present.Context}, {Kind: present.Context, Dest: "x.md"}}},
	} {
		req.Target, req.Engines = target, []string{"mock"}
		_, err := Materialize(context.Background(), engines.Registry(), cfg, req)
		require.ErrorIs(t, err, ErrMaterializeRequest, name)
	}
	_, err = Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Target: target, Engines: []string{"no-such-engine"}})
	require.ErrorContains(t, err, "no-such-engine")
}

// TestRunSyncPostSteps_MaterializesAtTheProjectRootItNames (tests 27 and
// 51, ruling 17b): the post-sync apply goes through Materialize with the
// project root as its explicit target and every other default — and it
// WRITES only with the user's consent (WriteProject, the CLI's --yes).
// Without it the same request runs as a dry run, nothing is written, and
// the plan comes back on the result as the refresh the sync skipped.
func TestRunSyncPostSteps_MaterializesAtTheProjectRootItNames(t *testing.T) {
	testsupport.Isolate(t)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	var got []MaterializeRequest
	planned := &MaterializeResult{Status: MaterializePlanned, Engines: []EngineOutcome{{Engine: "mock", Wrote: []string{"hooks"}}}}
	syncHooksStep = func(_ context.Context, _ engine.Registry, _ *config.Config, req MaterializeRequest) (*MaterializeResult, error) {
		got = append(got, req)
		if req.DryRun {
			return planned, nil
		}
		return &MaterializeResult{Status: MaterializeApplied}, nil
	}
	want := MaterializeRequest{Target: projectroot.WorkDir(), LeaveLaunchContext: true}

	consented := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), &config.Config{}, SyncDependenciesRequest{ApplyHooks: true, WriteProject: true}, consented, afero.NewMemMapFs())
	require.Len(t, got, 1)
	assert.Equal(t, want, got[0])
	assert.Nil(t, consented.RefreshSkipped, "a refresh that ran skipped nothing")

	unconsented := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), &config.Config{}, SyncDependenciesRequest{ApplyHooks: true}, unconsented, afero.NewMemMapFs())
	require.Len(t, got, 2)
	want.DryRun = true
	assert.Equal(t, want, got[1], "without consent the refresh only plans")
	assert.Same(t, planned, unconsented.RefreshSkipped, "the plan is reported as what the refresh skipped")
}

// TestRunSyncPostSteps_WithoutConsentWritesNothingAndSaysWhat (ruling 17b),
// with the real Materialize behind it: a sync the user did not pass --yes to
// leaves the project directory exactly as it was — no engine file, no
// ownership claim — and reports, per engine, the kinds the refresh would
// have written there.
func TestRunSyncPostSteps_WithoutConsentWritesNothingAndSaysWhat(t *testing.T) {
	cfg := hookedMaterializeCfg(t, "UNCONSENTED-MARK", "")
	root := t.TempDir()
	t.Setenv("CTXLOOM_ROOT", root)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = Materialize

	result := &SyncDependenciesResult{Total: 1}
	mark := strictness.Checkpoint()
	runSyncPostSteps(context.Background(), engines.Registry(), cfg, SyncDependenciesRequest{ApplyHooks: true}, result, afero.NewOsFs())
	require.Empty(t, strictness.Since(mark), "planning the refresh is no failure")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is written into the project directory")
	records, err := OwnershipRecords()
	require.NoError(t, err)
	writers, err := records.Writers()
	require.NoError(t, err)
	assert.Empty(t, writers, "nothing is claimed")

	require.NotNil(t, result.RefreshSkipped)
	assert.Equal(t, root, result.RefreshSkipped.Target)
	require.Len(t, result.RefreshSkipped.Engines, 1)
	assert.Equal(t, "claude-code", result.RefreshSkipped.Engines[0].Engine)
	assert.Contains(t, result.RefreshSkipped.Engines[0].Wrote, "hooks", "the team hook it would have written is named")
	assert.NotContains(t, result.RefreshSkipped.Engines[0].Wrote, "context", "launch-borne context is not part of the refresh")
}

// TestSyncMaterializeStep_AWholesaleFailureIsAnApplyFinding: the post-sync
// apply records an apply-class finding when Materialize errors or reports
// that no engine could be materialized (its errors named); a run that
// applied, partly or wholly, or materialized nothing, records none.
func TestSyncMaterializeStep_AWholesaleFailureIsAnApplyFinding(t *testing.T) {
	testsupport.Isolate(t)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	step := func(res *MaterializeResult, err error) report.Findings {
		syncHooksStep = func(context.Context, engine.Registry, *config.Config, MaterializeRequest) (*MaterializeResult, error) {
			return res, err
		}
		mark := strictness.Checkpoint()
		syncMaterializeStep(context.Background(), engines.Registry(), &config.Config{}, true)
		return strictness.Since(mark)
	}

	for name, res := range map[string]*MaterializeResult{
		"applied":    {Status: MaterializeApplied},
		"partial":    {Status: MaterializePartial, Errors: []string{"mock: disk full"}},
		"no result":  nil,
		"no engines": {Status: MaterializeNothing},
	} {
		require.Empty(t, step(res, nil), name)
	}

	failed := step(&MaterializeResult{Status: MaterializeFailed, Errors: []string{"mock: disk full", "claude-code: read-only"}}, nil)
	require.Len(t, failed, 1)
	assert.Equal(t, report.KindApply, failed[0].Kind)
	assert.Contains(t, failed[0].Text, "no engine could be materialized: mock: disk full; claude-code: read-only")

	errored := step(&MaterializeResult{Status: MaterializeFailed, Errors: []string{"mock: disk full"}}, errors.New("config generation gone"))
	require.Len(t, errored, 1)
	assert.Contains(t, errored[0].Text, "config generation gone", "the call's own error is what is reported")
	assert.NotContains(t, errored[0].Text, "no engine could be materialized")
}

// hookedMaterializeCfg is materializeCfg with the `reviewer` profile also
// shipping a session_start hook, so a delivery has a hooks kind to write.
func hookedMaterializeCfg(t *testing.T, mark, llm string) *config.Config {
	t.Helper()
	cfg := materializeCfg(t, mark, llm)
	appPaths := cfg.ToFixture().AppPaths
	require.NotEmpty(t, appPaths)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(bundletree.ProjectProfilesDir(t, appPaths[0]), "reviewer.yaml"),
		"select_tags:\n  - security\nhooks:\n  unified:\n    session_start:\n      - type: command\n        command: echo team-guardrail\n", 0o644)
	return cfg
}

// TestRunSyncPostSteps_LeavesLaunchContextOut: the post-sync refresh, with
// the real Materialize behind it, writes no context file for an engine
// whose context rides the launch (claude's argv channel) but still delivers
// its hooks, while an engine that opens its context file at rest (mock)
// still gets the context.
func TestRunSyncPostSteps_LeavesLaunchContextOut(t *testing.T) {
	cfg := hookedMaterializeCfg(t, "SYNC-MARK", "")
	root := t.TempDir()
	t.Setenv("CTXLOOM_ROOT", root)
	require.Equal(t, root, projectroot.WorkDir())
	reg := engines.Registry()
	claudeKind, ok := reg.Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	require.True(t, contextRidesTheLaunch(claudeKind.Root()), "claude's context rides the launch")
	mockKind, ok := reg.Lookup(engine.Name("mock"))
	require.True(t, ok)
	require.False(t, contextRidesTheLaunch(mockKind.Root()), "mock opens its context file at rest")

	var got []*MaterializeResult
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = func(ctx context.Context, reg engine.Registry, cfg *config.Config, req MaterializeRequest) (*MaterializeResult, error) {
		req.Engines = []string{"claude-code", "mock"}
		res, err := Materialize(ctx, reg, cfg, req)
		got = append(got, res)
		return res, err
	}
	runSyncPostSteps(context.Background(), reg, cfg, SyncDependenciesRequest{ApplyHooks: true, WriteProject: true}, &SyncDependenciesResult{Total: 1}, afero.NewOsFs())
	require.Len(t, got, 1)
	require.NotNil(t, got[0])
	require.Equal(t, MaterializeApplied, got[0].Status, "%v", got[0].Errors)

	assert.NoFileExists(t, filepath.Join(root, claude.ContextFileName), "claude's context rides the launch: no CLAUDE.md at rest")
	assert.Contains(t, fileIn(t, root, ".claude/settings.json"), "team-guardrail", "claude's hooks still refresh")
	assert.Contains(t, fileIn(t, root, mock.ContextFileName), "SYNC-MARK", "an engine reading its file at rest still gets the context")
	byEngine := map[string]EngineOutcome{}
	for _, o := range got[0].Engines {
		byEngine[o.Engine] = o
	}
	assert.NotContains(t, byEngine["claude-code"].Wrote, "context")
	assert.NotContains(t, byEngine["claude-code"].Released, "context", "the context kind is left untouched, not released")
	assert.Contains(t, byEngine["mock"].Wrote, "context")
}

// TestRunSyncPostSteps_APlainSyncLeavesNoContextAtRestToWarnOf: after the
// post-sync refresh for the default engine (claude, whose context rides the
// launch), the R5 run warning finds no materialized context in the project.
func TestRunSyncPostSteps_APlainSyncLeavesNoContextAtRestToWarnOf(t *testing.T) {
	cfg := hookedMaterializeCfg(t, "PLAIN-SYNC-MARK", "")
	root := t.TempDir()
	t.Setenv("CTXLOOM_ROOT", root)
	reg := engines.Registry()
	require.Equal(t, []string{"claude-code"}, ConfiguredEngines(reg, cfg), "the default engine is claude")
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = Materialize
	mark := strictness.Checkpoint()
	runSyncPostSteps(context.Background(), reg, cfg, SyncDependenciesRequest{ApplyHooks: true, WriteProject: true}, &SyncDependenciesResult{Total: 1}, afero.NewOsFs())
	require.Empty(t, strictness.Since(mark), "the refresh succeeded")
	assert.Contains(t, fileIn(t, root, ".claude/settings.json"), "team-guardrail", "the refresh ran")
	records, err := OwnershipRecords()
	require.NoError(t, err)
	atRest, err := delivery.ProjectContextClaims(records, root)
	require.NoError(t, err)
	assert.Empty(t, atRest, "no context at rest, so no R5 warning")
}
