package projectid

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registry file is read and saved through the Manager's afero.Fs, so a
// decorator on that fs sees both. Driven on a MemMapFs at a registry path
// absent from disk: a raw os read would miss the saved entry, and a raw save
// would fail on the missing directory or land on disk. The file lock is not
// exercised here — it is an OS lock by nature.
func TestManager_SavesAndLoadsTheRegistryThroughItsFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	root := filepath.Join(t.TempDir(), "never-on-disk")
	path := filepath.Join(root, "projects", "index.yaml")
	require.NoError(t, mem.MkdirAll(filepath.Dir(path), 0o755))
	m := &Manager{path: path, fs: mem}

	require.NoError(t, m.saveLocked(&registry{Projects: []Entry{{ProjectID: "high-tight-gulf", Path: "/work/p"}}}))

	got, err := m.ResolveByID("high-tight-gulf")
	require.NoError(t, err)
	require.NotNil(t, got, "the saved entry must be read back from the Manager's fs")
	assert.Equal(t, "/work/p", got.Path)

	_, err = os.Stat(root)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
