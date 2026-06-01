package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// setupRealProfile writes a real profile file under a temp .ctxloom and returns
// a config pointed at it. The profile transfer/content ops use the OS
// filesystem, so these tests use real temp dirs (not afero).
func setupRealProfile(t *testing.T) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	pdir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(pdir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "dev.yaml"), []byte("name: dev\nbundles:\n  - x\n"), 0644))
	return &config.Config{AppPaths: []string{appDir}}
}

func TestGetSetProfileContent(t *testing.T) {
	cfg := setupRealProfile(t)

	got, err := GetProfileContent(context.Background(), cfg, GetProfileContentRequest{Name: "dev"})
	require.NoError(t, err)
	assert.Contains(t, got.Content, "name: dev")

	_, err = SetProfileContent(context.Background(), cfg, SetProfileContentRequest{Name: "dev", Content: "name: dev\nbundles:\n  - y\n"})
	require.NoError(t, err)

	got, err = GetProfileContent(context.Background(), cfg, GetProfileContentRequest{Name: "dev"})
	require.NoError(t, err)
	assert.Contains(t, got.Content, "- y")

	// Invalid YAML is rejected, leaving the file intact.
	_, err = SetProfileContent(context.Background(), cfg, SetProfileContentRequest{Name: "dev", Content: "\tbad: [unterminated"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid profile")
}

func TestExportProfile(t *testing.T) {
	cfg := setupRealProfile(t)
	dest := t.TempDir()

	res, err := ExportProfile(context.Background(), cfg, ExportProfileRequest{Name: "dev", DestDir: dest})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dest, "dev.yaml"), res.Dest)
	_, err = os.Stat(res.Dest)
	require.NoError(t, err)

	_, err = ExportProfile(context.Background(), cfg, ExportProfileRequest{Name: "dev"})
	require.Error(t, err, "missing destination is an error")
}

func TestImportProfile(t *testing.T) {
	cfg := setupRealProfile(t)
	src := filepath.Join(t.TempDir(), "architect.yaml")
	require.NoError(t, os.WriteFile(src, []byte("name: architect\nbundles:\n  - z\n"), 0644))

	res, err := ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src})
	require.NoError(t, err)
	assert.Equal(t, "imported", res.Status)
	_, err = os.Stat(res.Dest)
	require.NoError(t, err)

	_, err = ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src, Force: true})
	require.NoError(t, err)
}
