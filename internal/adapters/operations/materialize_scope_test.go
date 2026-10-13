// Tests for the at-rest delivery's engine selection and its user-global
// scope guard (Materialize), and for the settings and context-file writers
// it drives.
package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// deliverManagedSettings delivers a backend's settings, hooks and MCP servers
// into dir at rest, through the placement core (Deliver) at the project root.
func deliverManagedSettings(t *testing.T, backend string, hooks *wire.HooksConfig, bundleMCP map[string]wire.MCPServer, manageStatusline bool, dir string, fs afero.Fs) {
	t.Helper()
	kind, ok := engines.Registry().Lookup(engine.Name(backend))
	require.True(t, ok)
	pkg := composite.Package{MCP: bundleMCP, Statusline: manageStatusline}
	if hooks != nil {
		pkg.Hooks = *hooks
	}
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{}, atRestPlacement(dir, kind.Root().Name, delivery.AllKinds()))
	require.NoError(t, err)
}

func TestManagedSettings_ClaudeCode(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	deliverManagedSettings(t, "claude-code", hooks, map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}}, true, "/project", fs)

	// Verify settings file was created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists, "settings.json should be created")

	// Verify MCP config file was created (even if empty)
	exists, err = afero.Exists(fs, "/project/.mcp.json")
	require.NoError(t, err)
	assert.True(t, exists, ".mcp.json should be created")

	// Read and verify content contains hooks
	content, err := afero.ReadFile(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "hooks")
	assert.Contains(t, string(content), "SessionStart")
}

// An unregistered engine has no declaration to read: the surfaces resolver
// refuses it by name rather than answering with an engine that carries
// nothing.
func TestEngineDeclaration_UnknownEngineIsRefused(t *testing.T) {
	_, err := engineDeclaration(engines.Registry(), "unknown-backend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-backend")
}

// TestManagedSettings_PreservesExistingSettings verifies that user customizations survive.
//
// NON-OBVIOUS: When ctxloom writes hooks to settings.json, it MERGES with existing
// content rather than overwriting. User's custom settings (like theme preferences)
// are preserved. Only ctxloom-managed sections (hooks, MCP servers) are updated.
//
// This is important because users may have manually configured settings that
// they don't want ctxloom to destroy on each hook application.
func TestManagedSettings_PreservesExistingSettings(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create existing settings with custom user config
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	existingContent := `{
  "customSetting": "userValue",
  "anotherSetting": 123
}`
	testsupport.WriteFileString(t, fs, "/project/.claude/settings.json", existingContent, 0o644)

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	deliverManagedSettings(t, "claude-code", hooks, nil, true, "/project", fs)

	// Read and verify user settings are preserved
	content, err := afero.ReadFile(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "customSetting")
	assert.Contains(t, string(content), "userValue")
	assert.Contains(t, string(content), "hooks")
}

// ==========================================================================
// Context file tests
// ==========================================================================
//
// Context files are the assembled markdown content that gets fed to the LLM.
// They contain fragments from profiles, deduplicated and ordered.

// TestWriteContextFile verifies that fragments are assembled into a context file.
// The file is stored in .ctxloom/cache/context/{hash}.md where hash is content-based.
func TestWriteContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()

	fragments := []*agent.Fragment{
		{Name: "frag1", Content: "First fragment content"},
		{Name: "frag2", Content: "Second fragment content"},
	}

	hash, err := agent.WriteContextFile("/project", fragments,
		agent.WithContextFS(fs))
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Verify context file was created
	contextPath := paths.CachePath(testBaseDir) + "/context/" + hash + ".md"
	exists, err := afero.Exists(fs, contextPath)
	require.NoError(t, err)
	assert.True(t, exists, "context file should be created")

	// Read and verify content
	content, err := afero.ReadFile(fs, contextPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "First fragment content")
	assert.Contains(t, string(content), "Second fragment content")
}

// TestWriteContextFile_Empty pins the distinction WriteContextFile must draw:
// NO fragments is legitimately nothing to deliver, while fragments that exist
// but assemble to zero bytes is a delivery failure. Reporting both
// as a silent success made "no context configured" indistinguishable from
// "every fragment resolved empty".
func TestWriteContextFile_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()

	hash, err := agent.WriteContextFile("/project", nil,
		agent.WithContextFS(fs))
	require.NoError(t, err, "no fragments at all: nothing was asked for")
	assert.Empty(t, hash)

	// Fragments that exist but carry no content: asked for context, got none.
	fragments := []*agent.Fragment{
		{Name: "empty", Content: ""},
	}
	hash, err = agent.WriteContextFile("/project", fragments,
		agent.WithContextFS(fs))
	require.ErrorIs(t, err, agent.ErrNoContext)
	assert.Empty(t, hash)
}

// TestMaterialize_NoEngineNamedIsTheConfiguredEngines: an unqualified
// materialize targets this project's configured engines.
func TestMaterialize_NoEngineNamedIsTheConfiguredEngines(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{}
	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Root: safefs.NewMem(fs), Target: "/project", Surfaces: []SurfaceSpec{{Kind: present.Settings}},
	})
	require.NoError(t, err)
	// DERIVED, not a list: a hand-listed expectation would restate which
	// engines exist today, and would pass just as happily if the default
	// fanned out over the whole registry instead.
	want := ConfiguredEngines(engines.Registry(), cfg)
	require.NotEmpty(t, want, "the fixture configures no engine; this comparison would be vacuous")
	var got []string
	for _, o := range res.Engines {
		got = append(got, o.Engine)
	}
	assert.ElementsMatch(t, want, got)
}

// TestMaterialize_NamedEngineLeavesOtherConfiguredEnginesUntouched pins
// engaging-nutmeg's settling criterion: a project that configures MORE THAN
// ONE engine (here claude-code via the default agent, and mock via a second
// agent) still writes exactly ONE engine's settings file when the caller
// names that one engine — the shape a per-engine "launch"/init write must
// have. TestMaterialize_NoEngineNamedIsTheConfiguredEngines pins the OTHER half
// (an unqualified materialize sweeps every configured engine); nothing
// before this test exercised a project with TWO configured engines to prove
// a NAMED apply does not also catch the second one — a regression here would
// mean a one-engine apply started writing (or, worse, silently dropped) a
// sibling engine's file.
func TestMaterialize_NamedEngineLeavesOtherConfiguredEnginesUntouched(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "echo hello", Type: "command"}},
		}}, config.Fixture{
			DefaultAgent: "default",
			Agents: map[string]agents.Agent{
				// Two agents, two DIFFERENT engines: default resolves to
				// claude-code via the project's primary label; helper is
				// explicitly bound to mock. ConfiguredEngines(cfg) must
				// therefore report both.
				"default": {Profiles: []string{"hooked"}},
				"helper":  {Profiles: []string{"hooked"}, LLM: "backup"},
			},
			LM: config.LMConfig{
				Configs: map[string]config.LLMConfig{
					"claude": {Type: "claude-code"},
					"backup": {Type: string(mock.Name)},
				},
				// Explicit, so PrimaryLabel doesn't fall back to "the only
				// entry" (which would be "backup"/mock with two configs
				// present, or ambiguous with more) — the default agent must
				// resolve to claude-code unambiguously.
				Defaults: config.RoleDefaults{Primary: "claude"},
			},
		}), nil
	}

	cfg, cerr := mockConfigLoader()
	require.NoError(t, cerr)
	require.ElementsMatch(t, []string{"claude-code", string(mock.Name)}, ConfiguredEngines(engines.Registry(), cfg),
		"fixture must configure BOTH engines, or this test proves nothing")

	result, err := Materialize(context.Background(), engines.Registry(), loaded(t, mockConfigLoader), MaterializeRequest{
		Engines:  []string{"claude-code"},
		Surfaces: surfacesBut(present.Context),
		Root:     safefs.NewMem(fs),
		Target:   tmpDir,
	})
	require.NoError(t, err)
	require.Len(t, result.Engines, 1, "a named materialize must report exactly the one engine it targeted")
	assert.Equal(t, "claude-code", result.Engines[0].Engine)

	claudeExists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, claudeExists, "the named engine's settings file must be written")

	// The defect engaging-nutmeg describes: an ahead-of-time sweep would have
	// written mock's settings too, even though nothing is launching (or
	// otherwise asking to materialize) mock in this call.
	mockExists, err := afero.Exists(fs, "/project/.mock/settings.json")
	require.NoError(t, err)
	assert.False(t, mockExists, "a named apply must leave OTHER configured engines' files untouched")
}

// ==========================================================================
// checkHookTargetScope tests
// ==========================================================================
//
// An at-rest install run from $HOME (no git root, no CTXLOOM_ROOT) used to
// silently write Claude Code's user-GLOBAL settings.json instead of a
// project's: workDir fell back to bare cwd == $HOME, and
// claude.ProjectSettingsPath(workDir) == claude.GlobalSettingsPath() in that
// case. That duplicated ctxloom's injected context (and the /clear banner)
// into every project. These tests pin the fix: Materialize refuses that
// collision unless Force is set.
//
// All four use testsupport.Isolate/ProjectDir so HOME is a fresh temp dir and
// the real ~/.claude is never at risk, on top of the injected memFS.

// TestMaterialize_RefusesHomeCollision is the canonical red case: Target set
// to HOME itself makes claude.ProjectSettingsPath(Target) resolve onto
// claude.GlobalSettingsPath(). Before the fix this silently succeeded and
// wrote to $HOME/.claude/settings.json; it must now error and write nothing.
func TestMaterialize_RefusesHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	_, err := Materialize(context.Background(), engines.Registry(), loaded(t, mockConfigLoader), MaterializeRequest{
		Engines:  []string{"claude-code"},
		Surfaces: surfacesBut(present.Context),
		Root:     safefs.NewMem(fs),
		Target:   home, // == the resolved Claude Code GLOBAL settings scope
	})
	require.Error(t, err, "materializing with Target==HOME must be refused")
	assert.Contains(t, err.Error(), "user-global settings")

	exists, existsErr := afero.Exists(fs, claude.ProjectSettingsPath(home))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write settings under HOME")
}

// TestMaterialize_ForceOverridesHomeCollision proves --force (Force:true) is
// the deliberate escape hatch: the same collision now warns loudly instead of
// erroring, and the apply proceeds and writes.
func TestMaterialize_ForceOverridesHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	var result *MaterializeResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = Materialize(context.Background(), engines.Registry(), loaded(t, mockConfigLoader), MaterializeRequest{
			Engines:  []string{"claude-code"},
			Surfaces: surfacesBut(present.Context),
			Root:     safefs.NewMem(fs),
			Target:   home,
			Force:    true,
		})
		require.NoError(t, err, "Force:true must let the collision proceed")
	})
	require.NotNil(t, result)
	assert.Equal(t, MaterializeApplied, result.Status)
	assert.Contains(t, stderr, "user-global settings", "Force must still warn loudly, not silently proceed")

	exists, err := afero.Exists(fs, claude.ProjectSettingsPath(home))
	require.NoError(t, err)
	assert.True(t, exists, "--force must actually write the settings file")
}

// TestMaterialize_TargetScopeGuardAppliesToAnyRegisteredBackend is the
// flow-level proof: the target-scope guard is a property of the
// composed engine registry, not a hardcoded per-engine list
// operations maintains its own copy of. Before this fix,
// checkHookTargetScope was a literal if/else naming each guarded backend and calling
// those engines' packages directly (the ADR-0026 violation) — a
// FURTHER backend with the identical $HOME==global collision class got NO
// protection no matter how it registered itself, because operations' copy of
// "which backends have this collision" could only ever be edited by hand.
//
// This test registers a synthetic backend nobody has hardcoded anywhere (a
// full descriptor through backends.Register, the exact seam a real engine's
// own package uses) and proves Materialize refuses the $HOME collision for it — generalizing the per-engine
// fix to "any registered backend", the property the old hardcoded branch
// could not have: it would have silently proceeded for this name.
func TestMaterialize_TargetScopeGuardAppliesToAnyRegisteredBackend(t *testing.T) {
	const fakeBackend = "t12-flow-test-engine"
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	// A collision class shaped exactly like the guarded engines' own: the
	// "project" path is a workDir join that happens to equal the "global"
	// path whenever workDir == HOME.
	reg := enginefixture.Install(t, enginefixture.Kind(fakeBackend, mock.WithHookGlobalScope(agent.HookGlobalScope{
		Paths: func(workDir string) (string, string, error) {
			return filepath.Join(workDir, ".t12fake", "settings.json"), filepath.Join(home, ".t12fake", "settings.json"), nil
		},
		Label: "the T12 fake engine's global settings",
	})))

	_, err := Materialize(context.Background(), reg, loaded(t, mockConfigLoader), MaterializeRequest{
		Engines:  []string{fakeBackend},
		Surfaces: surfacesBut(present.Context),
		Root:     safefs.NewMem(fs),
		Target:   home,
	})
	require.Error(t, err, "a backend registered with a hookGlobalScopePaths collision must be refused just like every descriptor-registered backend, with no operations-side edit for this backend name")
	assert.Contains(t, err.Error(), "the T12 fake engine's global settings")

	exists, existsErr := afero.Exists(fs, filepath.Join(home, ".t12fake", "settings.json"))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write the fake engine's settings under HOME")
}

// TestMaterialize_SubdirOfHomeIsNotACollision is the negative control: a real
// project living under HOME (e.g. ~/code/myproject) is not the global-scope
// collision — only Target==HOME itself is.
func TestMaterialize_SubdirOfHomeIsNotACollision(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := filepath.Join(home, "project")
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	result, err := Materialize(context.Background(), engines.Registry(), loaded(t, mockConfigLoader), MaterializeRequest{
		Engines:  []string{"claude-code"},
		Surfaces: surfacesBut(present.Context),
		Root:     safefs.NewMem(fs),
		Target:   projectDir,
	})
	require.NoError(t, err)
	assert.Equal(t, MaterializeApplied, result.Status)

	exists, err := afero.Exists(fs, claude.ProjectSettingsPath(projectDir))
	require.NoError(t, err)
	assert.True(t, exists, "a project subdirectory of HOME must apply normally")
}

// surfacesBut selects every kind except skip: for a fixture that declares
// hooks, settings or MCP and no fragments, whose context would assemble to
// nothing (which a materialize that selects context refuses).
func surfacesBut(skip present.Kind) []SurfaceSpec {
	var out []SurfaceSpec
	for _, k := range delivery.AllKinds() {
		if k != skip {
			out = append(out, SurfaceSpec{Kind: k})
		}
	}
	return out
}
