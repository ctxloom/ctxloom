//go:build !windows

package spool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The spool root is the spool's owner-only boundary: a root that already
// exists looser (created by an older binary, or by hand) is tightened by
// whichever call reaches it first, not trusted — MkdirAll alone leaves an
// existing directory's mode as it found it. Creating the layout from nothing
// makes every directory 0700, and what is written into it 0600.
func TestSpool_TheRootIsTightenedToOwnerOnly(t *testing.T) {
	for name, reach := range map[string]func(m PathMapper) error{
		"EnsureDirs": func(m PathMapper) error { return EnsureDirs(m, testHarp) },
		"ArmWake":    func(m PathMapper) error { _, err := ArmWake(m, testHarp); return err },
	} {
		t.Run(name, func(t *testing.T) {
			hostHome(t)
			m := NewHomeMapper()
			root, err := Root(m, testHarp)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(root, 0o755))
			require.NoError(t, os.Chmod(root, 0o755))

			require.NoError(t, reach(m))

			info, err := os.Stat(root)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		})
	}
}

func TestSpool_AFreshLayoutAndItsFilesAreOwnerOnly(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "x\n"})
	require.NoError(t, err)
	nonce, err := ArmWake(m, testHarp)
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
