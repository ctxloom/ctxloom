package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

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
		// Create minimal config structure
		require.NoError(t, fs.MkdirAll("/.ctxloom", 0755))
		require.NoError(t, afero.WriteFile(fs, "/.ctxloom/config.yaml", []byte("{}"), 0644))

		result, err := loadFreshConfig(fs, "/", nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}
