package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Contains(t, string(cfgData), "default: claude-code")

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
