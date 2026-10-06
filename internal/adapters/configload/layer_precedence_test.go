package configload

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Every layer (home < project < env < flag) may set every key; a higher
// layer wins. These pin the keys that once were refused by the layer they
// came from.

func TestLoad_HomeLayerSetsAgentPermissionsAndEnvHost(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	testsupport.WriteFile(t, fs, paths.ConfigPath(filepath.Join(home, ".ctxloom")), []byte(`schema_version: 7
agents:
  personal:
    llm: claude-code
    permissions:
      claude-code:
        mode: bypass
    env_host: false
`), 0644)
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("schema_version: 7\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)

	personal, ok := cfg.GetConfiguredAgents()["personal"]
	require.True(t, ok, "an agent declared in home config must take effect")
	assert.Equal(t, "bypass", personal.Permissions.Engines["claude-code"]["mode"])
	require.NotNil(t, personal.EnvHost)
	assert.False(t, *personal.EnvHost)
}

func TestLoad_EnvLayerSetsAgentPermissionsAndDefaultAgent(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`schema_version: 7
agents:
  reviewer:
    llm: claude-code
    permissions:
      claude-code:
        mode: plan
`), 0644)

	overrides := confload.Overrides{Env: map[string]any{
		"AGENTS_REVIEWER_PERMISSIONS_CLAUDE-CODE_MODE": "bypass",
		"DEFAULT_AGENT": "reviewer",
	}}
	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	reviewer, ok := cfg.GetConfiguredAgents()["reviewer"]
	require.True(t, ok)
	assert.Equal(t, "bypass", reviewer.Permissions.Engines["claude-code"]["mode"], "the env layer outranks the project file")
	assert.Equal(t, "reviewer", cfg.ToFixture().DefaultAgent)
}

func TestLoad_ProjectLayerSetsMachineFacts(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("schema_version: 7\neditor:\n  command: vim\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "vim", cfg.ToFixture().Editor.Command)
}

// TestOwnerUpdate_PersistsOnlyTheTargetFilesOwnLayer: a write transaction
// drafts from the file it writes, never the merged view, so an unrelated
// write cannot copy home's values (its agents, its machine paths) or an env
// override into the committed project file.
func TestOwnerUpdate_PersistsOnlyTheTargetFilesOwnLayer(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	testsupport.WriteFile(t, fs, paths.ConfigPath(filepath.Join(home, ".ctxloom")), []byte(`schema_version: 7
editor:
  command: vim
agents:
  personal:
    llm: claude-code
    env: [HOME_SECRET_NAME]
`), 0644)
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("schema_version: 7\nworkspace: none\n"), 0644)

	mgr := newUpdater(t, WithRoot(safefs.NewMem(fs)), WithAppDir(appDir),
		WithOverrides(confload.Overrides{Env: map[string]any{"OUTPUT_DIR": "/env/out"}}))
	require.NoError(t, mgr.Update(func(d *config.Draft) error {
		d.DefaultAgent = "reviewer"
		return nil
	}))

	data, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	persisted, err := config.ParseConfig(data)
	require.NoError(t, err)
	f := persisted.ToFixture()
	assert.Equal(t, "reviewer", f.DefaultAgent, "the write itself lands")
	assert.Equal(t, "none", f.Workspace, "the file's own values survive")
	assert.Empty(t, f.Editor.Command, "home's editor.command must not be copied into the project file")
	assert.NotContains(t, f.Agents, "personal", "home's agent must not be copied into the project file")
	assert.Empty(t, f.OutputDir, "an env override must not be persisted")
	assert.NotContains(t, string(data), "HOME_SECRET_NAME")
}
