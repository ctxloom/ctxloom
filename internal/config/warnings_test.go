package config

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// failOpenFs wraps an afero.Fs and fails Open/OpenFile for one path with a
// non-IsNotExist error, modeling an existing-but-unreadable config (EACCES, a
// directory in its place).
type failOpenFs struct {
	afero.Fs
	path string
}

func (f failOpenFs) Open(name string) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.Open(name)
}

func (f failOpenFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.OpenFile(name, flag, perm)
}

// An existing-but-unreadable config degrades with a kind-tagged read warning —
// the kind is what the strict startup gate aborts on.
func TestLoad_UnreadableConfigTaggedRead(t *testing.T) {
	base := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, base.MkdirAll(appDir, 0755))
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(base, cfgPath, []byte("llm: {}\n"), 0644))

	cfg, err := Load(WithFS(failOpenFs{Fs: base, path: cfgPath}), WithAppDir(appDir))
	require.NoError(t, err, "unreadable config must not hard-error the load itself")
	require.Len(t, cfg.warnings, 1)
	assert.Equal(t, WarnKindRead, cfg.warnings[0].Kind)
	assert.Contains(t, cfg.warnings[0].Text, "failed to read config")
}

// Broken YAML is tagged parse (plus the validator's validate warning), so the
// gate can distinguish a broken file from an absent one.
func TestLoad_BrokenYAMLTaggedParse(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("llm: [unclosed\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	require.NotEmpty(t, cfg.warnings)
	kinds := make(map[WarningKind]bool)
	for _, w := range cfg.warnings {
		kinds[w.Kind] = true
	}
	assert.True(t, kinds[WarnKindParse], "broken YAML must carry a parse-kind warning; got %v", cfg.warnings)
}

// An absent config file is fine: no warnings, no findings — strict mode only
// bites on present-but-broken files.
func TestLoad_AbsentConfigNoWarnings(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Empty(t, cfg.warnings)
}

// A lossy schema migration (the dropped compaction model at
// config_migrate.go's v2→v3 step) surfaces as a migration-lossy warning naming
// the key to fix, instead of a loose stderr line the gate cannot see.
func TestLoad_LossyMigrationTaggedMigrationLossy(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	// compaction.model with no compaction.llm and no primary label: the model
	// has no label to attach to and is dropped by migrateLLMv3.
	lossy := "llm:\n  compaction:\n    model: haiku\n"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(lossy), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	var lossyWarnings []Warning
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindMigrationLossy {
			lossyWarnings = append(lossyWarnings, w)
		}
	}
	require.Len(t, lossyWarnings, 1, "the dropped compaction model must be tagged migration-lossy; warnings: %v", cfg.warnings)
	assert.Contains(t, lossyWarnings[0].Text, "dropped compaction model")
	assert.Contains(t, lossyWarnings[0].Text, "llm.defaults.fast", "the message must name the key to fix")

	// The collector drains per load: a subsequent clean load carries nothing over.
	fs2 := afero.NewMemMapFs()
	require.NoError(t, fs2.MkdirAll(appDir, 0755))
	cfg2, err := Load(WithFS(fs2), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Empty(t, cfg2.warnings, "migration warnings must not leak into later loads")
}
