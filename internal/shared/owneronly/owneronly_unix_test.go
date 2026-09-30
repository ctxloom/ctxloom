//go:build !windows

package owneronly

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On unix owner-only is the mode: EnsureDir creates DirMode, and tightens a
// directory that already exists looser.
func TestEnsureDir_TightensALooseDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, EnsureDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, DirMode, info.Mode().Perm())
}

// Any group or other bit is exposure, refused as an *ExposedError naming
// the path and its mode.
func TestCheck_RefusesAnythingLooserThanOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		isDir bool
		mode  os.FileMode
	}{
		{"world-readable file", false, 0o644},
		{"group-readable file", false, 0o640},
		{"world-listable dir", true, 0o755},
		{"group-traversable dir", true, 0o710},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, EnsureDir(dir))
			f := filepath.Join(dir, "secret")
			require.NoError(t, os.WriteFile(f, []byte("x"), FileMode))
			loose := f
			if tc.isDir {
				loose = dir
			}
			require.NoError(t, os.Chmod(loose, tc.mode))

			err := Check(dir, f)
			var exposed *ExposedError
			require.True(t, errors.As(err, &exposed), "got %v", err)
			assert.Equal(t, loose, exposed.Path)
			assert.Contains(t, exposed.Why, "mode")
		})
	}
}
