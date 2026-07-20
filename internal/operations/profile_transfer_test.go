package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// memProfileFS seeds an in-memory profiles dir with one profile ("dev").
func memProfileFS(t *testing.T) (afero.Fs, *config.Config) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	pdir := paths.ProfilesPath(appDir)
	require.NoError(t, fs.MkdirAll(pdir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(pdir, "dev.yaml"),
		[]byte("description: dev\nbundles:\n  - x\n"), 0644))
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

func TestGetSetProfileContent(t *testing.T) {
	fs, cfg := memProfileFS(t)

	got, err := GetProfileContent(context.Background(), cfg, GetProfileContentRequest{Name: "dev", FS: fs})
	require.NoError(t, err)
	assert.Contains(t, got.Content, "description: dev")

	_, err = SetProfileContent(context.Background(), cfg, SetProfileContentRequest{Name: "dev", Content: "description: dev\nbundles:\n  - y\n", FS: fs})
	require.NoError(t, err)

	got, err = GetProfileContent(context.Background(), cfg, GetProfileContentRequest{Name: "dev", FS: fs})
	require.NoError(t, err)
	assert.Contains(t, got.Content, "- y")

	// Invalid YAML is rejected, leaving the file intact.
	_, err = SetProfileContent(context.Background(), cfg, SetProfileContentRequest{Name: "dev", Content: "\tbad: [unterminated", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid profile")
}

func TestExportProfile(t *testing.T) {
	fs, cfg := memProfileFS(t)

	res, err := ExportProfile(context.Background(), cfg, ExportProfileRequest{Name: "dev", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "dev.yaml"), res.Dest)
	exists, _ := afero.Exists(fs, res.Dest)
	assert.True(t, exists)

	_, err = ExportProfile(context.Background(), cfg, ExportProfileRequest{Name: "dev", FS: fs})
	require.Error(t, err, "missing destination is an error")
}

func TestImportProfile(t *testing.T) {
	fs, cfg := memProfileFS(t)
	src := "/incoming/architect.yaml"
	require.NoError(t, fs.MkdirAll("/incoming", 0755))
	require.NoError(t, afero.WriteFile(fs, src, []byte("description: architect\nbundles:\n  - z\n"), 0644))

	res, err := ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "imported", res.Status)
	exists, _ := afero.Exists(fs, res.Dest)
	assert.True(t, exists)

	_, err = ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src, FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src, Force: true, FS: fs})
	require.NoError(t, err)
}
