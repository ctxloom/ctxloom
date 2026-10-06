package coord

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The state dir holds the journals and endpoint.json (the consumer
// credential): it is created owner-only, on the coordinator's own fs. Its
// protection beyond that is the established home root's
// (paths.EnsureHomeRoots), not re-applied on every resolve.
func TestEnsureRootStateDir_CreatesTheDirOwnerOnly(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("USERPROFILE", "/home/u")
	root := safefs.NewMem(afero.NewMemMapFs())

	dir, err := ensureRootStateDir(root.Fs, "proj-key", "", "root-harp")
	require.NoError(t, err)
	require.NoError(t, root.Private.Check(dir))
}
