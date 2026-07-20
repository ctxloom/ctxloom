package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestOverrides_SurviveMemoReread pins loss channel 1: the memo re-read path
// (Load's stat-based cache validation) must still resolve overrides on every
// hit, not just the FIRST uncached read that populated the memo. Without
// applying overrides inside loadUncached (the single funnel), a second Load
// that hits the "stamp unchanged" fast path would silently drop them.
func TestOverrides_SurviveMemoReread(t *testing.T) {
	writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	SetOverrides(confload.Overrides{
		Env: map[string]any{"DEFAULT_AGENT": "beta"},
	})
	t.Cleanup(ResetOverrides)

	first, err := Load()
	require.NoError(t, err)
	require.Equal(t, "beta", first.DefaultAgent)

	// A second Load with NOTHING changed on disk must still carry the
	// override — this is the memo re-read path (ambientStamp unchanged).
	second, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "beta", second.DefaultAgent, "an override must survive a memo re-read, not just the first uncached load")
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
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\ndefault_agent: alpha\n"), 0o644))

	SetOverrides(confload.Overrides{
		Env: map[string]any{"DEFAULT_AGENT": "beta"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "beta", cfg.DefaultAgent, "an explicit WithAppDir load bypasses the memo but must still resolve process overrides")
}

// TestOverrides_AppliedOnLoadFresh pins the mutator entry point: LoadFresh
// (agent/llm/mcp/tooling writers) must resolve overrides exactly like the
// no-arg Load does.
func TestOverrides_AppliedOnLoadFresh(t *testing.T) {
	writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	SetOverrides(confload.Overrides{
		Env: map[string]any{"DEFAULT_AGENT": "beta"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := LoadFresh()
	require.NoError(t, err)
	assert.Equal(t, "beta", cfg.DefaultAgent)
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
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\ndefault_agent: alpha\n"), 0o644))

	cfg, err := Load(WithAppDir(appDir), WithOverrides(confload.Overrides{
		Env: map[string]any{"DEFAULT_AGENT": "seam-only"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "seam-only", cfg.DefaultAgent)

	// The process-wide value must be untouched: a plain reload with no
	// WithOverrides sees no override at all.
	assert.Equal(t, confload.Overrides{}, CurrentOverrides())
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
	writeProjectConfig(t, `version: 6
llm:
  configs:
    big:
      type: claude-code
      model: opus
      env:
        GEMINI_API_KEY: secret
`)

	// An unrelated env override elsewhere in the tree must not perturb the
	// case-sensitive map -- exercising the override resolution path
	// alongside the file layers, not instead of them.
	SetOverrides(confload.Overrides{
		Env: map[string]any{"DEFAULT_AGENT": "alpha"},
	})
	t.Cleanup(ResetOverrides)

	cfg, err := Load()
	require.NoError(t, err)

	entry, ok := cfg.LM.Configs["big"]
	require.True(t, ok)
	assert.Equal(t, "secret", entry.Body["env"].(map[string]any)["GEMINI_API_KEY"],
		"GEMINI_API_KEY must survive the full load with its exact casing -- viper must never have touched this map")
	assert.Equal(t, "alpha", cfg.DefaultAgent)
}

// TestInstallOverridesFromFlags_CapturesEnvAndChangedFlag exercises
// internal/cli/root.go's PersistentPreRun hook end to end: it must read both
// an env override and a --config-set flag and install them process-wide,
// ready to be resolved by the very next Load. The FlagSet ALSO carries an
// unrelated business flag sharing its name with a real config key
// ("runtime", as `agent set`/`container_cmd` declare) to prove
// InstallOverridesFromFlags never scans it — only --config-set contributes.
func TestInstallOverridesFromFlags_CapturesEnvAndChangedFlag(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("CTXLOOM_CONFIG_DEFAULT_AGENT", "from-env")

	appDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\nruntime: host\n"), 0o644))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(confload.ConfigSetFlagName, nil, "")
	fs.String("runtime", "", "") // an ordinary command flag, NOT a config override
	require.NoError(t, fs.Set(confload.ConfigSetFlagName, "default_agent=from-flag"))
	require.NoError(t, fs.Set("runtime", "container"))

	require.NoError(t, InstallOverridesFromFlags(fs))
	t.Cleanup(ResetOverrides)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.DefaultAgent, "--config-set must beat env")
	assert.Equal(t, "host", cfg.Runtime, "the --runtime BUSINESS flag must never be scanned as a config override")
}
