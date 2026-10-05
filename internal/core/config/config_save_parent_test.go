package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// The config read never creates the app dir, so the first config write is
// what creates it: an Owner.Update in an empty HOME lands here with
// ~/.ctxloom absent.
func TestSaveLocked_CreatesAbsentAppDir(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), AppDirName)
	configPath := filepath.Join(appDir, ConfigFileName+".yaml")
	c := &Config{source: SourceHome}

	require.NoError(t, c.saveLocked(afero.NewOsFs(), configPath))
	_, err := os.Stat(configPath)
	require.NoError(t, err)
}
