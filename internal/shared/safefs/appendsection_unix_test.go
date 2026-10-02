//go:build !windows

// Unix mode bits: Windows has only a read-only attribute, so a perm is not observable there.

package safefs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// TestAppendSection_ANewFileTakesThePermAnExistingOneKeepsItsMode: the
// perm is for a file this write creates; a file that already stood keeps
// the mode its owner gave it — appending never widens or narrows it.
func TestAppendSection_ANewFileTakesThePermAnExistingOneKeepsItsMode(t *testing.T) {
	fs := afero.NewOsFs()
	dir := t.TempDir()

	created := filepath.Join(dir, "new.md")
	require.NoError(t, safefs.AppendSection(fs, created, []byte("section"), 0o600))
	info, err := os.Stat(created)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	theirs := filepath.Join(dir, "theirs.md")
	require.NoError(t, os.WriteFile(theirs, []byte("hand-written\n"), 0o644))
	require.NoError(t, safefs.AppendSection(fs, theirs, []byte("section"), 0o600))
	info, err = os.Stat(theirs)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the owner's mode is kept")
	body, err := os.ReadFile(theirs)
	require.NoError(t, err)
	require.Equal(t, "hand-written\n\nsection\n", string(body))
}
