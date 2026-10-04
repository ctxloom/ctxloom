//go:build !windows

package safefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unix-only because Windows reports no owner-only permission bits at all.
func TestBatchCreatesAPrivateFile(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "deep", "er", "f")
	b := NewBatch(fs, noLock)
	b.Edit(path, appendText("x"))
	_, err := b.Commit()
	require.NoError(t, err)
	info, err := fs.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a file the batch creates is owner-only")
}
