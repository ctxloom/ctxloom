//go:build !windows

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
)

// A hooks dir that cannot be created fails the workspace: launching without
// it would leave the container free to create one the host's git then runs.
func TestGitDirMounts_UncreatableHooksDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root creates the hooks dir through any permission")
	}
	dir, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, false)
	require.NoError(t, os.Chmod(common, 0o555))
	t.Cleanup(func() { _ = os.Chmod(common, 0o755) })

	_, err := gitDirMounts(context.Background(), fakeRuntime{name: "docker"}, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.ErrorIs(t, err, os.ErrPermission)
}

// An entry that cannot be told present or absent fails the workspace rather
// than going unshadowed.
func TestGitDirMounts_UnreadableEntryFails(t *testing.T) {
	dir, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, true)
	index := filepath.Join(common, "index")
	require.NoError(t, os.Remove(index))
	require.NoError(t, os.Symlink("index", index)) // a loop: stat fails with ELOOP

	_, err := gitDirMounts(context.Background(), fakeRuntime{name: "docker"}, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist)
}
