package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestConfigSaveIsWriteBackForm: a config is saved in the encoding an upgrade
// write-back would give it, whether the save writes a fresh file or patches a
// hand-written one.
func TestConfigSaveIsWriteBackForm(t *testing.T) {
	for name, seed := range map[string]string{
		"first write": "",
		"patch":       "# hand edited\nschema_version: 7\nmcp:\n  servers:\n    srv:\n      command: x # mine\n",
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			appDir := "/proj/.ctxloom"
			require.NoError(t, fs.MkdirAll(appDir, 0o755))
			path := paths.ConfigPath(appDir)
			if seed != "" {
				testsupport.WriteFileString(t, fs, path, seed, 0o644)
			}
			cfg := &Config{
				appPaths:     []string{appDir},
				source:       SourceHome,
				editor:       EditorConfig{Command: "vim", Args: []string{"-p"}},
				defaultAgent: "reviewer",
			}
			cfg.SetRoot(safefs.NewMem(fs))
			require.NoError(t, cfg.saveLocked(fs, path))

			saved, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			yamlform.RequireWriteBackForm(t, saved)
		})
	}
}
