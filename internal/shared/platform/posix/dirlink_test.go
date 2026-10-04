package posix

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The link names its target RELATIVELY, so it resolves wherever the two trees
// are mounted side by side.
func TestLinkDir_IsRelative(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "native", "claude", "projects")
	link := filepath.Join(root, "home", "claude", "projects")
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))

	require.NoError(t, Linker{}.LinkDir(link, target))

	got, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("..", "..", "native", "claude", "projects"), got)
	ok, err := Linker{}.LinksTo(link, target)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestLinksTo_SaysWhatIsThere(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "t")
	require.NoError(t, os.MkdirAll(target, 0o700))

	_, err := Linker{}.LinksTo(filepath.Join(root, "absent"), target)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing there is told apart from something else there")

	real := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	ok, err := Linker{}.LinksTo(real, target)
	require.NoError(t, err)
	assert.False(t, ok, "a real directory is not the link")

	elsewhere := filepath.Join(root, "elsewhere")
	require.NoError(t, os.Symlink(root, elsewhere))
	ok, err = Linker{}.LinksTo(elsewhere, target)
	require.NoError(t, err)
	assert.False(t, ok, "a link to another target is not the link")
}

func TestLinksResolveInContainers(t *testing.T) {
	assert.True(t, Linker{}.LinksResolveInContainers())
}

// LinkTarget is the absolute directory a relative link names; a real
// directory is not a link.
func TestLinkTarget_ResolvesTheRelativeLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "native", "claude", "projects")
	link := filepath.Join(root, "home", "claude", "projects")
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
	require.NoError(t, Linker{}.LinkDir(link, target))

	got, err := Linker{}.LinkTarget(link)
	require.NoError(t, err)
	assert.Equal(t, target, got)

	_, err = Linker{}.LinkTarget(target)
	assert.Error(t, err)
}
