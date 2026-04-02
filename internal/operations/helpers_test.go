package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// testBaseDir is the base directory used in tests across the operations package
const testBaseDir = "/project/" + paths.AppDirName

func TestGetFS(t *testing.T) {
	t.Run("returns provided fs", func(t *testing.T) {
		memFs := afero.NewMemMapFs()
		result := getFS(memFs)
		assert.Equal(t, memFs, result)
	})

	t.Run("returns OsFs when nil", func(t *testing.T) {
		result := getFS(nil)
		assert.NotNil(t, result)
	})
}

func TestLoadFreshConfig(t *testing.T) {
	t.Run("returns test config if provided", func(t *testing.T) {
		testCfg := &config.Config{}
		result, err := loadFreshConfig(nil, "", testCfg)
		require.NoError(t, err)
		assert.Equal(t, testCfg, result)
	})

	t.Run("loads config when test config is nil", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		// Create minimal config structure using root-level .ctxloom
		rootAppDir := "/" + paths.AppDirName
		require.NoError(t, fs.MkdirAll(rootAppDir, 0755))
		require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(rootAppDir), []byte("{}"), 0644))

		result, err := loadFreshConfig(fs, "/", nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}
