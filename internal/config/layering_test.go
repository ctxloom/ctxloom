package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeLayers seeds a project config.yaml at "/proj/.ctxloom" and, when
// homeBody is non-empty, a home config.yaml under an isolated HOME — both on
// the SAME fake fs, so Load(WithFS(fs), WithAppDir(...)) exercises real
// layering without ever touching the developer's actual ~/.ctxloom. It
// returns the loaded Config.
func writeLayers(t *testing.T, homeBody, projectBody string) *Config {
	t.Helper()
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()

	projectAppDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(projectAppDir), []byte(projectBody), 0644))
	if homeBody != "" {
		homeAppDir := filepath.Join(home, AppDirName)
		require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(homeAppDir), []byte(homeBody), 0644))
	}

	cfg, err := Load(WithFS(fs), WithAppDir(projectAppDir))
	require.NoError(t, err)
	return cfg
}

// TestLoad_ProjectInheritsHomeKeys is the new-behavior pin D3 exists for: a
// project config.yaml that never mentions a key must inherit it from home,
// where the pre-layering project-XOR-home resolution silently dropped it
// (home was never even read once a project config.yaml existed). Both keys
// here are ScopeMachine (internal/config/layerscope), which home is allowed
// to carry — unlike `workspace` (ScopeShared), which this test used to
// exercise before layerscope closed home's ability to gap-fill a
// project-policy key (see TestLoad_EscalationPath3_HomeCannotEscalateProjectAgent
// for that closure's own dedicated test).
func TestLoad_ProjectInheritsHomeKeys(t *testing.T) {
	cfg := writeLayers(t,
		"version: 6\nagent_turn_cap: 3\nruntime: container\n",
		"version: 6\ndefault_agent: dev\nagents:\n  dev:\n    profiles: [go-developer]\n",
	)

	assert.Equal(t, 3, cfg.agentTurnCap, "a home-only key must be inherited by a project that never sets it")
	assert.Equal(t, "container", cfg.runtime, "same for a second home-only key")
	assert.Equal(t, "dev", cfg.defaultAgent, "the project's own key is unaffected by layering")
}

// TestLoad_ProjectOverridesHomeKey is TestLoad_ProjectInheritsHomeKeys'
// mirror: a key BOTH layers set resolves to the project's value, not home's —
// layering adds inheritance, it does not change who wins a genuine conflict.
func TestLoad_ProjectOverridesHomeKey(t *testing.T) {
	cfg := writeLayers(t,
		"version: 6\nworkspace: worktree\n",
		"version: 6\nworkspace: none\n",
	)

	assert.Equal(t, "none", cfg.workspace, "project explicitly sets workspace: none, beating home's worktree")
}

// TestLoad_HomeAndProjectDeepMergeNestedSection proves the deep-merge rule
// (D1) reaches inside a config.yaml section, not just top-level keys: a
// project overriding one llm.configs label must not drop a SIBLING label
// only home defines.
func TestLoad_HomeAndProjectDeepMergeNestedSection(t *testing.T) {
	cfg := writeLayers(t,
		"version: 6\nllm:\n  configs:\n    big: { type: claude-code, model: opus }\n    small: { type: claude-code, model: haiku }\n",
		"version: 6\nllm:\n  configs:\n    big: { type: claude-code, model: sonnet }\n",
	)

	require.Contains(t, cfg.lm.Configs, "big")
	require.Contains(t, cfg.lm.Configs, "small")
	assert.Equal(t, "sonnet", cfg.lm.Configs["big"].Body["model"], "project's override of the shared label wins")
	assert.Equal(t, "haiku", cfg.lm.Configs["small"].Body["model"], "home's sibling label survives the deep merge")
}

// TestLoad_NoProjectFound_HomeIsSingleSource pins that when findAppDir never
// resolves a project at all, home is the single effective source exactly as
// before layering existed — there is no separate layer to merge it under, so
// this path is untouched by D1-D3.
func TestLoad_NoProjectFound_HomeIsSingleSource(t *testing.T) {
	// A direct unit test of resolveConfigLayerPaths (rather than driving the
	// real findAppDir walk-up from a t.TempDir()) so this pin is hermetic: the
	// walk-up climbs real ancestor directories up to filesystem root, so a
	// stray .ctxloom left anywhere above the OS temp dir by unrelated host
	// activity would make an end-to-end version of this test flaky for
	// reasons that have nothing to do with layering. What actually matters —
	// "SourceHome yields no separate home layer" — is exactly what
	// resolveConfigLayerPaths decides, so pin that directly.
	appPath := "/home/someone/.ctxloom"
	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(appPath, SourceHome)

	assert.Equal(t, paths.ConfigPath(appPath), projectConfigPath)
	assert.Empty(t, homeConfigPath,
		"when findAppDir already fell back to home (SourceHome), home IS the single effective source — "+
			"there is no separate lower-precedence layer to read a second time")
}

// TestResolveConfigLayerPaths_DedupsWhenHomeEqualsProject covers the other
// SourceProject edge: an explicit/resolved project dir that happens to BE the
// home dir (a home-rooted CTXLOOM_ROOT, or a bare `--app-dir ~/.ctxloom`)
// must not read the same file twice as two "different" layers.
func TestResolveConfigLayerPaths_DedupsWhenHomeEqualsProject(t *testing.T) {
	home := testsupport.Isolate(t)
	homeAppDir := filepath.Join(home, AppDirName)

	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(homeAppDir, SourceProject)

	assert.Equal(t, paths.ConfigPath(homeAppDir), projectConfigPath)
	assert.Empty(t, homeConfigPath, "project dir == home dir must collapse to a single-file read, not a doubled one")
}
