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
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
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
// 51): the post-sync apply goes through Materialize with the project root
// as its explicit target and every other default.
func TestRunSyncPostSteps_MaterializesAtTheProjectRootItNames(t *testing.T) {
	testsupport.Isolate(t)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	var got []MaterializeRequest
	syncHooksStep = func(_ context.Context, _ engine.Registry, _ *config.Config, req MaterializeRequest) (*MaterializeResult, error) {
		got = append(got, req)
		return &MaterializeResult{Status: MaterializeApplied}, nil
	}
	runSyncPostSteps(context.Background(), engines.Registry(), &config.Config{}, SyncDependenciesRequest{ApplyHooks: true}, &SyncDependenciesResult{Total: 1}, afero.NewMemMapFs())
	require.Len(t, got, 1)
	assert.Equal(t, MaterializeRequest{Target: projectroot.WorkDir()}, got[0])
}
