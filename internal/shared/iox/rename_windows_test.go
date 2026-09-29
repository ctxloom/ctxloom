package iox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRename_WaitsOutAHandleHeldOnTheDestination: Windows refuses to replace
// a file another handle holds open, and Rename outlasts a brief holder where
// a bare rename fails outright.
func TestRename_WaitsOutAHandleHeldOnTheDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new")
	dst := filepath.Join(dir, "live")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o644))

	holder, err := os.Open(dst)
	require.NoError(t, err)
	require.Error(t, os.Rename(src, dst), "precondition: a bare rename over an open file is refused")
	released := time.AfterFunc(150*time.Millisecond, func() { _ = holder.Close() })
	t.Cleanup(func() { released.Stop(); _ = holder.Close() })

	require.NoError(t, Rename(afero.NewOsFs(), src, dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
}
