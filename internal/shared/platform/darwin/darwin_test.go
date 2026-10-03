package darwin

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentsDir_IsHomeDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := OS{}.DocumentsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Documents"), got)
}

// macOS has no per-user tmpfs, whatever the environment claims.
func TestPrivateTmpfs_None(t *testing.T) {
	_, ok := OS{}.PrivateTmpfs(func(string) string { return "/run/user/501" })
	assert.False(t, ok)
}
