package configload

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The project's permission defaults (`permissions:` at the top level of
// config.yaml: the engine-neutral fields) layer like every other key.

// TestProjectPermissions_HonoredFromProjectLayer: a project config that
// declares the key is read back through the accessor the resolution chain
// consults.
func TestProjectPermissions_HonoredFromProjectLayer(t *testing.T) {
	cfg := writeLayers(t, "", "schema_version: 7\npermissions:\n  sandbox: full\n")

	assert.Equal(t, "full", cfg.GetPermissions().Sandbox,
		"a project config's declared permission default must be honored")
}

// TestProjectPermissions_HomeLayerFillsAnUndeclaredProject: the home config
// declares a posture, the project declares nothing, and home's value applies.
func TestProjectPermissions_HomeLayerFillsAnUndeclaredProject(t *testing.T) {
	cfg := writeLayers(t,
		"schema_version: 7\npermissions:\n  sandbox: full\n",
		"schema_version: 7\n",
	)

	assert.Equal(t, "full", cfg.GetPermissions().Sandbox)
	assert.Empty(t, cfg.GetWarnings(), "a home-layer permissions block is ordinary configuration")
}

// TestProjectPermissions_EnvOverridesTheProject: the env layer outranks the
// project file.
func TestProjectPermissions_EnvOverridesTheProject(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("schema_version: 7\npermissions:\n  sandbox: workspace\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir),
		WithOverrides(confload.Overrides{Env: map[string]any{"PERMISSIONS_SANDBOX": "full"}}))
	require.NoError(t, err)

	assert.Equal(t, "full", cfg.GetPermissions().Sandbox)
}
