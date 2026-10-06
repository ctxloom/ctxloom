package configload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeLayers seeds a project config.yaml at "/proj/.ctxloom" and, when
// homeBody is non-empty, a home config.yaml under an isolated HOME — both on
// the SAME fake fs, so Load(WithRoot(safefs.NewMem(fs)), WithAppDir(...)) exercises real
// layering without ever touching the developer's actual ~/.ctxloom. It
// returns the loaded config.Config.
func writeLayers(t *testing.T, homeBody, projectBody string) *config.Config {
	t.Helper()
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	projectAppDir := seedLayers(t, fs, home, homeBody, projectBody)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(projectAppDir))
	require.NoError(t, err)
	return cfg
}

// seedLayers is writeLayers' seeding half for a test that owns its HOME and
// fs (it needs the home path afterwards, or writes through a Manager rather
// than a Load): the project config.yaml lands at "/proj/.ctxloom" and, when
// homeBody is non-empty, the home config.yaml under home. Returns the project
// app dir.
func seedLayers(t *testing.T, fs afero.Fs, home, homeBody, projectBody string) string {
	t.Helper()
	projectAppDir := "/proj/.ctxloom"
	testsupport.WriteFileString(t, fs, paths.ConfigPath(projectAppDir), projectBody, 0644)
	if homeBody != "" {
		homeAppDir := filepath.Join(home, config.AppDirName)
		testsupport.WriteFileString(t, fs, paths.ConfigPath(homeAppDir), homeBody, 0644)
	}
	return projectAppDir
}

// TestLoad_ProjectInheritsHomeKeys is the new-behavior pin D3 exists for: a
// project config.yaml that never mentions a key must inherit it from home,
// where the pre-layering project-XOR-home resolution silently dropped it
// (home was never even read once a project config.yaml existed).
func TestLoad_ProjectInheritsHomeKeys(t *testing.T) {
	cfg := writeLayers(t,
		"schema_version: 7\ndelegation:\n  concurrency: 3\nruntime: container\nworkspace: none\n",
		"schema_version: 7\ndefault_agent: dev\nagents:\n  dev:\n    profiles: [go-developer]\n",
	)

	assert.Equal(t, 3, cfg.ToFixture().Delegation.Concurrency, "a home-only key must be inherited by a project that never sets it")
	assert.Equal(t, "container", cfg.ToFixture().Runtime, "same for a second home-only key")
	assert.Equal(t, "none", cfg.ToFixture().Workspace, "same for a project-policy key")
	assert.Equal(t, "dev", cfg.ToFixture().DefaultAgent, "the project's own key is unaffected by layering")
}

// TestLoad_ProjectOverridesHomeKey is TestLoad_ProjectInheritsHomeKeys'
// mirror: a key BOTH layers set resolves to the project's value, not home's —
// layering adds inheritance, it does not change who wins a genuine conflict.
func TestLoad_ProjectOverridesHomeKey(t *testing.T) {
	cfg := writeLayers(t,
		"schema_version: 7\nworkspace: worktree\n",
		"schema_version: 7\nworkspace: none\n",
	)

	assert.Equal(t, "none", cfg.ToFixture().Workspace, "project explicitly sets workspace: none, beating home's worktree")
}

// TestLoad_HomeAndProjectDeepMergeNestedSection proves the deep-merge rule
// (D1) reaches inside a config.yaml section, not just top-level keys: a
// project overriding one llm.configs label must not drop a SIBLING label
// only home defines.
func TestLoad_HomeAndProjectDeepMergeNestedSection(t *testing.T) {
	cfg := writeLayers(t,
		"schema_version: 7\nllm:\n  configs:\n    big: { type: claude-code, model: opus }\n    small: { type: claude-code, model: haiku }\n",
		"schema_version: 7\nllm:\n  configs:\n    big: { type: claude-code, model: sonnet }\n",
	)

	require.Contains(t, cfg.ToFixture().LM.Configs, "big")
	require.Contains(t, cfg.ToFixture().LM.Configs, "small")
	assert.Equal(t, "sonnet", cfg.ToFixture().LM.Configs["big"].Body["model"], "project's override of the shared label wins")
	assert.Equal(t, "haiku", cfg.ToFixture().LM.Configs["small"].Body["model"], "home's sibling label survives the deep merge")
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
	// "config.SourceHome yields no separate home layer" — is exactly what
	// resolveConfigLayerPaths decides, so pin that directly.
	appPath := "/home/someone/.ctxloom"
	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(appPath, config.SourceHome)

	assert.Equal(t, paths.ConfigPath(appPath), projectConfigPath)
	assert.Empty(t, homeConfigPath,
		"when findAppDir already fell back to home (config.SourceHome), home IS the single effective source — "+
			"there is no separate lower-precedence layer to read a second time")
}

// TestResolveConfigLayerPaths_DedupsWhenHomeEqualsProject covers the other
// config.SourceProject edge: an explicit/resolved project dir that happens to BE the
// home dir (a home-rooted CTXLOOM_ROOT, or a bare `--app-dir ~/.ctxloom`)
// must not read the same file twice as two "different" layers.
func TestResolveConfigLayerPaths_DedupsWhenHomeEqualsProject(t *testing.T) {
	home := testsupport.Isolate(t)
	homeAppDir := filepath.Join(home, config.AppDirName)

	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(homeAppDir, config.SourceProject)

	assert.Equal(t, paths.ConfigPath(homeAppDir), projectConfigPath)
	assert.Empty(t, homeConfigPath, "project dir == home dir must collapse to a single-file read, not a doubled one")
}

// TestLoad_ExplicitAppDirEqualToHome_ResolvesSourceHome: an explicit appDir
// that IS ~/.ctxloom is home acting alone, matching resolveConfigLayerPaths'
// dedup on the read side.
func TestLoad_ExplicitAppDirEqualToHome_ResolvesSourceHome(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	homeAppDir := filepath.Join(home, config.AppDirName)
	testsupport.WriteFile(t, fs, paths.ConfigPath(homeAppDir), []byte("schema_version: 7\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(homeAppDir))
	require.NoError(t, err)
	assert.Equal(t, config.SourceHome, cfg.ToFixture().Source, "an explicit appDir that IS the home directory must resolve as config.SourceHome")
}

// TestLoad_ExplicitAppDirDifferentFromHome_StaysSourceProject is the
// negative case: an ordinary project directory (not home) resolves
// config.SourceProject.
func TestLoad_ExplicitAppDirDifferentFromHome_StaysSourceProject(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	projectAppDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(projectAppDir), []byte("schema_version: 7\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(projectAppDir))
	require.NoError(t, err)
	assert.Equal(t, config.SourceProject, cfg.ToFixture().Source)
}

// TestManagerUpdate_TargetingHomeDirectly_PersistsMachineValues: a Manager
// pointed straight at ~/.ctxloom writes a machine path
// (llm.configs.*.binary_path) and it survives a reload.
func TestManagerUpdate_TargetingHomeDirectly_PersistsMachineValues(t *testing.T) {
	home := testsupport.Isolate(t)
	homeAppDir := filepath.Join(home, config.AppDirName)
	require.NoError(t, os.MkdirAll(homeAppDir, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(homeAppDir), []byte("schema_version: 7\n"), 0o644))

	mgr := newUpdater(t, WithAppDir(homeAppDir))
	err := mgr.Update(func(d *config.Draft) error {
		if d.LM.Configs == nil {
			d.LM.Configs = map[string]config.LLMConfig{}
		}
		entry := d.LM.Configs["big"]
		entry.Type = "mock"
		entry.Body = map[string]any{"binary_path": "/opt/engines/mock"}
		d.LM.Configs["big"] = entry
		return nil
	})
	require.NoError(t, err)

	reloaded, err := Load(WithAppDir(homeAppDir))
	require.NoError(t, err)
	got, ok := reloaded.GetLLMEntry("big")
	require.True(t, ok)
	assert.Equal(t, "/opt/engines/mock", got.Body["binary_path"],
		"a machine path written straight to home must survive the save")
}
