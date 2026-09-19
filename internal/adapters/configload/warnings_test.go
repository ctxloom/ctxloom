package configload

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"

	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	testsupport.WriteFile(t, base, cfgPath, []byte("llm: {}\n"), 0644)

	cfg, err := Load(WithFS(failOpenFs{Fs: base, path: cfgPath}), WithAppDir(appDir))
	require.NoError(t, err, "unreadable config must not hard-error the load itself")
	require.Len(t, cfg.GetWarnings(), 1)
	assert.Equal(t, config.WarnKindRead, cfg.GetWarnings()[0].Kind)
	assert.Contains(t, cfg.GetWarnings()[0].Text, "failed to read config")
}

// Broken YAML in a PRESENT file is a refusal naming the file (Part 1.8: an
// absent layer is the shipped default; a present unparsable one is never
// silently dropped).
func TestLoad_BrokenYAMLTaggedParse(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("llm: [unclosed\n"), 0644)

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.ErrorIs(t, err, ErrUnparsableLayer, "a PRESENT file that cannot be parsed is refused, never dropped with a warning")
	assert.Contains(t, err.Error(), paths.ConfigPath(appDir), "the refusal names the file")
	assert.Nil(t, cfg)
}

// An absent config file is fine: no warnings, no findings — strict mode only
// bites on present-but-broken files.
func TestLoad_AbsentConfigNoWarnings(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Empty(t, cfg.GetWarnings())
}
