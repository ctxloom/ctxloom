//go:build !windows

package spool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// A layout created from nothing makes every directory, the root included,
// 0700, and what is written into it 0600. Its boundary beyond that is the
// established sessions root (paths.EnsureHomeRoots).
func TestSpool_AFreshLayoutAndItsFilesAreOwnerOnly(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(afero.NewOsFs(), m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "x\n"})
	require.NoError(t, err)
	nonce, err := ArmWake(afero.NewOsFs(), m, testHarp)
	require.NoError(t, err)

	root, err := Root(m, testHarp)
	require.NoError(t, err)
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		info, err := d.Info()
		require.NoError(t, err)
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		require.Equal(t, want, info.Mode().Perm(), p)
		return nil
	}))
	msg, err := m.Resolve(ref)
	require.NoError(t, err)
	require.FileExists(t, msg)
	require.FileExists(t, filepath.Join(root, filepath.FromSlash(wakeDirName), nonce))
}
