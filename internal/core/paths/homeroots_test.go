package paths_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Each named home root is established owner-only, a loosened one is
// tightened again, and nothing else under the home is touched.
func TestEnsureHomeRoots_EstablishesEachNamedRootOwnerOnly(t *testing.T) {
	home := t.TempDir() // a path only: the fs below is in memory
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := safefs.NewMem(afero.NewMemMapFs())

	require.NoError(t, paths.EnsureHomeRoots(root.Private))
	var dirs []string
	for _, dir := range []func() (string, error){paths.HomeCoordDir, paths.HomeSessionsDir, paths.HomeRecordsDir} {
		d, err := dir()
		require.NoError(t, err)
		require.NoError(t, root.Private.Check(d), "%s must be owner-only", d)
		dirs = append(dirs, d)
	}

	require.NoError(t, root.Fs.Chmod(dirs[1], 0o755))
	require.NoError(t, paths.EnsureHomeRoots(root.Private))
	require.NoError(t, root.Private.Check(dirs...))
}
