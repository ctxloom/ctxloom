//go:build acceptance

package acceptance

import (
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// departingStore is a session store whose departing session's runner tears
// its delivery down WHILE the walk is inside it: the config is unlinked
// between the directory listing that names it and the lstat behind its
// Info, and the departing session's config dir is removed between the
// listing that names it and the read that would descend into it. Both are
// the interleavings a killed owner's runner children (SIGTERMed by their
// parent-death signal, reversing their own deliveries) produce against a
// walk taken the moment the owner has exited.
type departingStore struct {
	fs.FS
	root, departing string
}

func (s departingStore) ReadDir(name string) ([]fs.DirEntry, error) {
	real := filepath.Join(s.root, filepath.FromSlash(name))
	if name == path.Join(s.departing, "home", "gone", mock.ConfigDirName) {
		if err := os.RemoveAll(real); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(s.FS, name)
	for i, e := range entries {
		if name == path.Join(s.departing, "home", "mock", mock.ConfigDirName) && e.Name() == mockMCPFileName {
			entries[i] = unlinkedBeforeInfo{DirEntry: e, path: filepath.Join(real, e.Name())}
		}
	}
	return entries, err
}

type unlinkedBeforeInfo struct {
	fs.DirEntry
	path string
}

func (e unlinkedBeforeInfo) Info() (fs.FileInfo, error) {
	if err := os.Remove(e.path); err != nil {
		return nil, err
	}
	return e.DirEntry.Info()
}

// TestDeliveredConfigsSurvivesADepartingSessionsTeardown: a config that a
// departing session's teardown takes mid-walk is not delivered, and the walk
// goes on to report the configs that are.
func TestDeliveredConfigsSurvivesADepartingSessionsTeardown(t *testing.T) {
	for name, engineHome := range map[string]string{
		"its config is unlinked before its lstat":           "mock",
		"its config dir is removed before the walk descends": "gone",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write := func(rel string) string {
				p := filepath.Join(root, filepath.FromSlash(rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
				return p
			}
			write(path.Join("departing", "home", engineHome, mock.ConfigDirName, mockMCPFileName))
			kept := write(path.Join("resumed", "home", "mock", mock.ConfigDirName, mockMCPFileName))

			got, err := deliveredConfigsIn(departingStore{FS: os.DirFS(root), root: root, departing: "departing"}, root)

			require.NoError(t, err)
			require.Equal(t, []string{kept}, slices.Collect(maps.Keys(got)), "only the config still there is delivered")
		})
	}
}
