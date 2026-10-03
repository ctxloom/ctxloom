package cli

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// These helpers read through the fs they are handed. Each case puts the file
// ONLY in a MemMapFs, under a path that does not exist on disk, so an answer
// read from the OS filesystem instead is the wrong answer.
func TestCLIHelpers_ReadThroughTheGivenFs(t *testing.T) {
	fsys := afero.NewMemMapFs()
	root := filepath.Join(t.TempDir(), "mem-only")

	cfg := filepath.Join(root, "config.yaml")
	testsupport.WriteFile(t, fsys, cfg, []byte("x: 1\n"), 0o600)
	exists, err := configFileExists(fsys, cfg)
	require.NoError(t, err)
	require.True(t, exists, "configFileExists")

	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, fsys.MkdirAll(appDir, 0o755))
	require.True(t, ctxloomDirExists(fsys, appDir), "ctxloomDirExists")

	bundle := filepath.Join(root, "a.yaml")
	testsupport.WriteFile(t, fsys, bundle, []byte("a"), 0o600)
	files, err := expandDistillFiles(fsys, []string{filepath.Join(root, "*.yaml")})
	require.NoError(t, err)
	require.Equal(t, []string{bundle, cfg}, files, "expandDistillFiles globs the given fs")

	const harp = "mem-only-harp"
	transcript, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(filepath.Dir(transcript), 0o755))
	testsupport.WriteFile(t, fsys, transcript, []byte("{}\n"), 0o600)
	_, size, ok := statHarpFile(fsys, harp, paths.HarpCanonicalTranscriptPath)
	require.True(t, ok, "statHarpFile")
	require.EqualValues(t, 3, size)
	require.NoError(t, verifyHarpDirExists(fsys, harp), "verifyHarpDirExists")
}
