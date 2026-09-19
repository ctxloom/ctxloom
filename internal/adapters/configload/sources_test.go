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

// TestSources_Read_ParityWithTodaysLoader computes its expectation by calling
// the loader this package replaces, so the two halves are compared on the
// full exported state (config.Fixture) rather than on a hand-written subset.
func TestSources_Read_ParityWithTodaysLoader(t *testing.T) {
	cases := map[string]string{
		"empty file": "",
		"agents and llm registry": `version: 3
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
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fs := hermetic(t)
			require.NoError(t, afero.WriteFile(fs, appDir+"/config.yaml", []byte(body), 0o644))

			want, err := config.Load(config.WithAppDir(appDir), config.WithFS(fs))
			require.NoError(t, err)

			src, err := configload.New(nil, nil, configload.WithFS(fs), configload.WithAppDir(appDir))
			require.NoError(t, err)
			got, warnings, err := src.Read(context.Background())
			require.NoError(t, err)

			assert.Equal(t, want.ToFixture(), got.ToFixture())
			assert.Equal(t, want.GetWarnings(), warnings)
		})
	}
}

// The override chain (env, then --config-set) is a CONSTRUCTOR input: the
// Sources built from one flag set and one environment yield what today's
// process-global funnel yields for the same inputs.
func TestSources_Read_OverridesFromFlagsAndEnv_ParityWithTodaysFunnel(t *testing.T) {
	fs := hermetic(t)
	require.NoError(t, afero.WriteFile(fs, appDir+"/config.yaml", []byte("version: 3\ndefault_agent: fromfile\n"), 0o644))

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringArray(confload.ConfigSetFlagName, nil, "")
	require.NoError(t, flags.Parse([]string{"--" + confload.ConfigSetFlagName, "default_agent=fromflag"}))
	const envKey, envVal = "CTXLOOM_CONFIG_WORKSPACE", "worktree"

	t.Setenv(envKey, envVal)
	require.NoError(t, config.InstallOverridesFromFlags(flags))
	t.Cleanup(config.ResetOverrides)
	want, err := config.Load(config.WithAppDir(appDir), config.WithFS(fs))
	require.NoError(t, err)
	config.ResetOverrides()

	src, err := configload.New(flags, []string{envKey + "=" + envVal}, configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, _, err := src.Read(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "fromflag", got.GetDefaultAgent(), "the flag override reached the value")
	assert.Equal(t, "worktree", got.GetWorkspace(), "the env override reached the value")
	assert.Equal(t, want.ToFixture(), got.ToFixture())
}
