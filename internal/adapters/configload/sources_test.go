package configload_test

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
)

const appDir = "/proj/.ctxloom"

// hermetic pins HOME to an empty directory so the home layer is genuinely
// absent and the test's only inputs are the memfs files it writes.
func hermetic(t *testing.T) afero.Fs {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CTXLOOM_ROOT", "")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	return fs
}

func TestSources_Read_AbsentLayers_YieldShippedDefaultWithoutError(t *testing.T) {
	fs := hermetic(t)
	src, err := configload.New(nil, nil, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)

	cfg, warnings, err := src.Read(context.Background())
	require.NoError(t, err, "an ABSENT layer is the shipped default, not a fault")
	require.NotNil(t, cfg)
	assert.Empty(t, warnings, "nothing to warn about when no file exists")
	assert.NotEmpty(t, cfg.GetDefaultLLM(), "the shipped default registry resolves a primary role")
	assert.Equal(t, appDir, cfg.GetAppDir())
}

func TestSources_Read_PresentUnparsableLayer_RefusesNamingTheFile(t *testing.T) {
	fs := hermetic(t)
	path := appDir + "/config.yaml"
	require.NoError(t, afero.WriteFile(fs, path, []byte("default_agent: [unclosed\n  : nonsense\n"), 0o644))
	src, err := configload.New(nil, nil, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)

	cfg, _, err := src.Read(context.Background())
	require.Error(t, err, "a PRESENT file that cannot be parsed is refused, never silently dropped")
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), path, "the refusal names the file")
}

// TestSources_Read_LayersFilesIntoTheValue pins the reading half's result on
// a single project layer: the file's values reach the Config through the
// same decode a single-file parse uses, and a user registry is never
// overlaid with the shipped default entries.
func TestSources_Read_LayersFilesIntoTheValue(t *testing.T) {
	fs := hermetic(t)
	require.NoError(t, afero.WriteFile(fs, appDir+"/config.yaml", []byte(`version: 6
default_agent: coder
agents:
  coder:
    profiles: [dev]
    llm: big
llm:
  defaults:
    primary: big
  configs:
    big:
      backend: claude-code
      model: opus
workspace: worktree
`), 0o644))

	src, err := configload.New(nil, nil, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, warnings, err := src.Read(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "coder", got.GetDefaultAgent())
	assert.Equal(t, "worktree", got.GetWorkspace())
	assert.Equal(t, "big", got.PrimaryLabel())
	agent, ok := got.Agent("coder")
	require.True(t, ok)
	assert.Equal(t, []string{"dev"}, agent.Profiles)
	assert.Len(t, got.GetLLMLabels(), 1, "a user registry is not overlaid with the shipped defaults")
	assert.Equal(t, got.GetWarnings(), warnings, "Read returns the value's own warnings")
}

// The override chain (env, then --config-set) is a CONSTRUCTOR input: one
// flag set and one environment, captured at New, reach every Read's value.
// The env key is a machine-scope one — env may not set a project-scoped
// key, and that refusal is a warning on the value, not a silent drop.
func TestSources_Read_OverridesFromFlagsAndEnv_ReachTheValue(t *testing.T) {
	fs := hermetic(t)
	require.NoError(t, afero.WriteFile(fs, appDir+"/config.yaml", []byte("version: 6\ndefault_agent: fromfile\n"), 0o644))

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringArray(confload.ConfigSetFlagName, nil, "")
	require.NoError(t, flags.Parse([]string{"--" + confload.ConfigSetFlagName, "default_agent=fromflag"}))
	environ := []string{"CTXLOOM_CONFIG_SESSION_REAP_AGE=7d", "CTXLOOM_CONFIG_WORKSPACE=worktree"}

	src, err := configload.New(flags, environ, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, warnings, err := src.Read(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "fromflag", got.GetDefaultAgent(), "the flag override reached the value")
	assert.Equal(t, "7d", got.SessionReapAge(), "the machine-scope env override reached the value")
	assert.Empty(t, got.GetWorkspace(), "env may not set a project-scope key")
	var scopeWarned bool
	for _, w := range warnings {
		scopeWarned = scopeWarned || w.Kind == config.WarnKindLayerScope
	}
	assert.True(t, scopeWarned, "the refused env override is a layer-scope warning on the value")
}

// A nil environment is NO environment: the process's real variables never
// leak into a Sources that was not handed them.
func TestSources_New_NilEnviron_IgnoresProcessEnvironment(t *testing.T) {
	fs := hermetic(t)
	t.Setenv("CTXLOOM_CONFIG_SESSION_REAP_AGE", "99d")

	src, err := configload.New(nil, nil, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, _, err := src.Read(context.Background())
	require.NoError(t, err)

	assert.NotEqual(t, "99d", got.SessionReapAge())
}
