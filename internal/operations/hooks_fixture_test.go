package operations

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// cfgWithProfileHooks builds a config whose one selected profile declares h.
//
// A directory profile is the only place a user can declare hooks, so it is the
// only honest fixture for "this run has hooks". The profile file is written into
// the SAME afero.Fs the test drives, and SetFS makes the loader read it there --
// GetProfileDirs and profiles.WithFS both take the config's fs, so a memfs test
// stays a memfs test and never touches the real filesystem.
//
// Pass the fs the test already uses; appDir is where .ctxloom lives in it.
func cfgWithProfileHooks(t *testing.T, fs afero.Fs, appDir string, h wire.HooksConfig, f config.Fixture) *config.Config {
	t.Helper()
	body, err := yaml.Marshal(map[string]any{"hooks": h})
	require.NoError(t, err)
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, fs.MkdirAll(profilesDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(profilesDir, "hooked.yaml"), body, 0o644))

	f.AppPaths = append(f.AppPaths, appDir)
	if f.DefaultAgent == "" {
		f.DefaultAgent = "default"
		f.Agents = map[string]agents.Agent{"default": {Profiles: []string{"hooked"}}}
	}
	cfg := config.NewFixture(f)
	cfg.SetFS(fs)
	cfg.DisableCompanionProbe()
	return cfg
}
