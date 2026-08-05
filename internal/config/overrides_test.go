package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestOverrides_SurviveMemoReread pins loss channel 1: the memo re-read path
// (Load's stat-based cache validation) must still resolve overrides on every
// hit, not just the FIRST uncached read that populated the memo. Without
// applying overrides inside loadUncached (the single funnel), a second Load
// that hits the "stamp unchanged" fast path would silently drop them.
func TestOverrides_SurviveMemoReread(t *testing.T) {
	// editor.command is ScopePreference (see internal/config/layerscope):
	// harmless personal taste, settable from every layer, unlike
	// default_agent (ScopeShared -- project+flag only, since which agent a
	// bare `ctxloom run` resolves is project policy an env var must not be
	// able to redirect). Any env-settable field proves the same memo-reread
	// mechanics; this one keeps the test's original "arbitrary string"
	// shape without tripping the new scope gate.
	writeProjectConfig(t, "version: 6\neditor:\n  command: alpha\n")

	SetOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "beta"},
	})
	t.Cleanup(ResetOverrides)

	first, err := Load()
	require.NoError(t, err)
	require.Equal(t, "beta", first.editor.Command)

	// A second Load with NOTHING changed on disk must still carry the
	// override — this is the memo re-read path (ambientStamp unchanged).
	second, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "beta", second.editor.Command, "an override must survive a memo re-read, not just the first uncached load")
}

// TestOverrides_AppliedOnExplicitAppDirLoad pins loss channel 3: an explicit
// WithAppDir/WithFS load (agent_run's per-spawn config resolution, a worktree
// load, --app-dir) bypasses the ambient memo ENTIRELY (Load's len(opts)>0
// branch goes straight to loadUncached) — it must still resolve the
// process-wide overrides, since it goes through the same funnel.
func TestOverrides_AppliedOnExplicitAppDirLoad(t *testing.T) {
	home := testsupport.Isolate(t)
	appDir := filepath.Join(home, "otherproject", AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	// editor.command (ScopePreference) stands in for the override target --
	// see TestOverrides_SurviveMemoReread's comment for why default_agent
	// (ScopeShared) no longer fits this purpose.
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\neditor:\n  command: alpha\n"), 0o644))

	SetOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "beta"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "beta", cfg.editor.Command, "an explicit WithAppDir load bypasses the memo but must still resolve process overrides")
}

// TestOverrides_AppliedOnLoadFresh pins the mutator entry point: LoadFresh
// (agent/llm/mcp/tooling writers) must resolve overrides exactly like the
// no-arg Load does.
func TestOverrides_AppliedOnLoadFresh(t *testing.T) {
	// editor.command (ScopePreference) stands in for the override target --
	// see TestOverrides_SurviveMemoReread's comment for why default_agent
	// (ScopeShared) no longer fits this purpose.
	writeProjectConfig(t, "version: 6\neditor:\n  command: alpha\n")

	SetOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "beta"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := LoadFresh()
	require.NoError(t, err)
	assert.Equal(t, "beta", cfg.editor.Command)
}

// TestOverrides_WithOverridesTestSeamDoesNotMutateProcessState proves
// WithOverrides resolves a specific Overrides for ONE call without touching
// the process-wide value SetOverrides installs -- the test seam the API
// promises, so tests can inject overrides without polluting every other test
// sharing the same binary.
func TestOverrides_WithOverridesTestSeamDoesNotMutateProcessState(t *testing.T) {
	home := testsupport.Isolate(t)
	appDir := filepath.Join(home, "proj", AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	// editor.command (ScopePreference) stands in for the override target --
	// see TestOverrides_SurviveMemoReread's comment for why default_agent
	// (ScopeShared) no longer fits this purpose.
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\neditor:\n  command: alpha\n"), 0o644))

	cfg, err := Load(WithAppDir(appDir), WithOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "seam-only"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "seam-only", cfg.editor.Command)

	// The process-wide value must be untouched: a plain reload with no
	// WithOverrides sees no override at all.
	assert.Equal(t, confload.Overrides{}, currentOverrides())
}

// TestConfig_EnvMapKeyCasePreserved is the end-to-end guard for the package's
// ABSOLUTE CONSTRAINT: viper must never decode a config file, because it
// lowercases every map key -- which would corrupt a case-sensitive
// pass-through map like an LLM backend's `env` block (see ctxloom commit
// 26f96c7: a backend's `env: {GEMINI_API_KEY: ...}` reached the launched
// process as `gemini_api_key`, so the engine never saw its credential). This
// exercises the FULL four-layer load (file + env-override resolution +
// remarshal/unmarshal into cfg) to prove the constraint holds end to end, not
// just in isolated unit tests of the file-reading step.
func TestConfig_EnvMapKeyCasePreserved(t *testing.T) {
	// llm.configs.*.env is ScopeMachine (internal/config/layerscope):
	// credential passthrough, where a value living in the COMMITTED,
	// multi-author project file is a leaked secret. So this now has to live
	// in the HOME layer to be honored at all -- writeProjectConfig's
	// isolated $HOME (via testsupport.ProjectDir) is where it's written.
	writeProjectConfig(t, "version: 6\n")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	homeAppDir := filepath.Join(home, AppDirName)
	require.NoError(t, os.MkdirAll(homeAppDir, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(homeAppDir), []byte(`version: 6
llm:
  configs:
    big:
      type: claude-code
      model: opus
      env:
        GEMINI_API_KEY: secret
`), 0o644))

	// An unrelated env override elsewhere in the tree must not perturb the
	// case-sensitive map -- exercising the override resolution path
	// alongside the file layers, not instead of them. editor.command is
	// ScopePreference (settable from every layer, unlike default_agent).
	SetOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "alpha"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := Load()
	require.NoError(t, err)

	entry, ok := cfg.lm.Configs["big"]
	require.True(t, ok)
	assert.Equal(t, "secret", entry.Body["env"].(map[string]any)["GEMINI_API_KEY"],
		"GEMINI_API_KEY must survive the full load with its exact casing -- viper must never have touched this map")
	assert.Equal(t, "alpha", cfg.editor.Command)
}

// TestInstallOverridesFromFlags_CapturesEnvAndChangedFlag exercises
// internal/cli/root.go's PersistentPreRun hook end to end: it must read both
// an env override and a --config-set flag and install them process-wide,
// ready to be resolved by the very next Load. The FlagSet ALSO carries an
// unrelated business flag sharing its name with a real config key
// ("workspace", as `run --workspace` declares) to prove
// InstallOverridesFromFlags never scans it — only --config-set contributes.
// workspace (not runtime, this test's original target) is ScopeShared, which
// --config-set keeps but a top-level `runtime:` in the committed PROJECT
// file this test writes could no longer carry at all (ScopeMachine).
func TestInstallOverridesFromFlags_CapturesEnvAndChangedFlag(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("CTXLOOM_CONFIG_DEFAULT_AGENT", "from-env")

	appDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\nworkspace: none\n"), 0o644))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(confload.ConfigSetFlagName, nil, "")
	fs.String("workspace", "", "") // an ordinary command flag, NOT a config override
	require.NoError(t, fs.Set(confload.ConfigSetFlagName, "default_agent=from-flag"))
	require.NoError(t, fs.Set("workspace", "worktree"))

	require.NoError(t, InstallOverridesFromFlags(fs))
	t.Cleanup(ResetOverrides)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.defaultAgent, "--config-set must beat env")
	assert.Equal(t, "none", cfg.workspace, "the --workspace BUSINESS flag must never be scanned as a config override")
}

// Three initialization errors in this file are handled three DIFFERENT
// ways: InstallOverridesFromFlags discards the validator error entirely with
// no log, loadUncached zap-warns it, and mergeDefaultConfig returns silently.
//
// The first limb is no longer true: both validator sites were changed so this
// one now zap-warns with a stated rationale, and loadUncached escalates to a
// fatal-class cfg.warnings entry. The remaining divergence is therefore
// deliberate and carries its reasoning in the code at each site — a compile
// failure DURING A LOAD disables validation for every config in the process
// (fatal-class), the same failure during override RESOLUTION only degrades
// KnownPath to "nothing recognized" (a warning), and an unreadable embedded
// default is fault tolerance by design ("a malformed/unreadable default never
// blocks startup"). Three different consequences, three different responses.
//
// loadUncached's half is pinned by TestLoad_SchemaCompileFailureProducesWarning.
// This is the half that had nothing, so a future "consistency" pass that
// silences it, or that promotes it to a hard failure, fails here instead of
// changing startup behaviour unobserved.
func TestInstallOverridesFromFlags_SchemaCompileFailureDegradesNotFails(t *testing.T) {
	testsupport.Isolate(t)
	orig := newConfigValidatorFn
	newConfigValidatorFn = func() (*schema.ConfigValidator, error) {
		return nil, errors.New("simulated schema compile failure")
	}
	defer func() { newConfigValidatorFn = orig }()

	appDir := t.TempDir()
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\n"), 0o644))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(confload.ConfigSetFlagName, nil, "")
	require.NoError(t, fs.Set(confload.ConfigSetFlagName, "default_agent=from-flag"))

	require.NoError(t, InstallOverridesFromFlags(fs),
		"a schema-compile failure must degrade override resolution, never fail the invocation")
	t.Cleanup(ResetOverrides)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.defaultAgent,
		"the override must still be installed and applied with no schema knowledge — degrading KnownPath is not the same as dropping the override")
}
