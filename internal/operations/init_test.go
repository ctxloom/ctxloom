package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestInitializeProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"

	res, err := InitializeProject(context.Background(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "initialized", res.Status)

	cfgData, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err, "config.yaml should be written")
	body := string(cfgData)
	// The written config carries a v3 registry, not the old `llm.default` scalar.
	assert.Contains(t, body, "configs:")
	assert.Contains(t, body, "type: claude-code")
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
	// `role` is registry-only metadata stripped on write — it must never persist.
	assert.NotContains(t, body, "role:")

	bundlesDir, err := afero.DirExists(fs, paths.BundlesPath(appDir))
	require.NoError(t, err)
	assert.True(t, bundlesDir, "bundles dir should be created")

	remotesExists, err := afero.Exists(fs, paths.RemotesPath(appDir))
	require.NoError(t, err)
	assert.True(t, remotesExists, "remotes.yaml should be written")
}

func TestInitializeProject_RequiresAppDir(t *testing.T) {
	_, err := InitializeProject(context.Background(), InitializeProjectRequest{Engine: "claude-code", FS: afero.NewMemMapFs()})
	require.Error(t, err)
}

func TestBuildInitialConfig(t *testing.T) {
	tests := []struct {
		name        string
		engine      string
		wantPrimary string // primary label → backend/model
		wantBackend string
		wantModel   string
		wantFast    string // fast label → backend/model
		wantFastBE  string
		wantFastMod string
	}{
		{
			name: "claude-code wires opus + haiku", engine: "claude-code",
			wantPrimary: "claude-code", wantBackend: "claude-code", wantModel: "claude-opus-4-8",
			wantFast: "claude-fast", wantFastBE: "claude-code", wantFastMod: "claude-haiku-4-5-20251001",
		},
		{
			// The shipped registry pins no model for antigravity: agy's own
			// configured default applies, and the lone role-marked entry
			// serves both the primary and fast roles.
			name: "antigravity wires its single role-marked entry to both roles", engine: "antigravity",
			wantPrimary: "antigravity", wantBackend: "antigravity", wantModel: "",
			wantFast: "antigravity", wantFastBE: "antigravity", wantFastMod: "",
		},
		{
			name: "engine without role markers falls back to a single entry", engine: "codex",
			wantPrimary: "codex", wantBackend: "codex", wantModel: "",
			wantFast: "codex", wantFastBE: "codex", wantFastMod: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := BuildInitialConfig(tt.engine)
			require.NoError(t, err)

			// `role` is registry-only — it must be stripped from the written config.
			assert.NotContains(t, string(data), "role:")

			cfg, err := config.ParseConfig(data)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPrimary, cfg.LM.Defaults.Primary)
			assert.Equal(t, tt.wantFast, cfg.LM.Defaults.Fast)

			// Round-tripped config resolves the expected backend + model per role.
			be, model := cfg.ResolveLLM(cfg.PrimaryLabel())
			assert.Equal(t, tt.wantBackend, be)
			assert.Equal(t, tt.wantModel, model)
			assert.Equal(t, tt.wantBackend, cfg.GetDefaultLLM())
			assert.Equal(t, tt.wantModel, cfg.GetDefaultLLMModel())

			assert.Equal(t, tt.wantFastBE, cfg.GetCompactionLLM())
			assert.Equal(t, tt.wantFastMod, cfg.GetCompactionModel())
		})
	}
}
