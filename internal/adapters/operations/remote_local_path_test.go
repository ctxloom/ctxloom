package operations

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// A remote spelled as a filesystem path is a LOCAL repository. The owner's
// ruling: resolve it against the working directory, check a repository is
// really there, and store it as the file:// URL it names — or refuse, naming
// the file:// spelling. It is never guessed into a network host.

// localBareRepo creates a bare repository at dir/name and returns its absolute
// path as the working directory will resolve it.
func localBareRepo(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	_, err := git.PlainInit(p, true)
	require.NoError(t, err)
	return p
}

func fileURL(abs string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func addLocal(t *testing.T, registry *remote.Registry, raw string) (*fakeCloner, error) {
	t.Helper()
	cache := &fakeCloner{}
	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "local",
		URL:      raw,
		Registry: registry,
		Fetcher:  remote.NewMockFetcher(),
		Cache:    cache,
	})
	return cache, err
}

func TestAddRemote_ResolvesALocalPathToTheFileURLOfTheRepositoryThere(t *testing.T) {
	root := t.TempDir()
	repo := localBareRepo(t, root, "bundles.git")
	sub := filepath.Join(root, "work")
	require.NoError(t, os.Mkdir(sub, 0o755))

	for _, tc := range []struct {
		name, cwd, raw string
	}{
		{"dot-slash", root, "./bundles.git"},
		{"dot-dot-slash", sub, "../bundles.git"},
		{"absolute", sub, repo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			registry, _ := setupTestRegistry(t)

			cache, err := addLocal(t, registry, tc.raw)
			require.NoError(t, err)

			rem, err := registry.Get("local")
			require.NoError(t, err)
			assert.Equal(t, fileURL(repo), rem.URL, "the stored remote is the file:// URL of the repository the path names")
			assert.Equal(t, []string{fileURL(repo)}, cache.urls, "the clone reaches the same local repository")
		})
	}
}

// A path naming no repository is refused before anything is registered or
// cloned: there is nothing local to resolve it to, and guessing a host is the
// one answer the ruling forbids.
func TestAddRemote_RefusesALocalPathWithNoRepositoryThere(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "plain-dir"), 0o755))
	t.Chdir(root)

	for _, raw := range []string{"./missing.git", "../nowhere-" + filepath.Base(root), filepath.Join(root, "missing.git"), "./plain-dir"} {
		t.Run(raw, func(t *testing.T) {
			registry, _ := setupTestRegistry(t)

			cache, err := addLocal(t, registry, raw)
			require.ErrorIs(t, err, refuri.ErrSchemelessPath)
			assert.False(t, registry.Has("local"), "nothing is registered for a path with no repository")
			assert.Empty(t, cache.urls, "nothing is cloned for a path with no repository")
		})
	}
}

func TestEditRemote_ResolvesALocalPathURL(t *testing.T) {
	root := t.TempDir()
	repo := localBareRepo(t, root, "moved.git")
	t.Chdir(root)
	reg := editRemoteRegistry(t)

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{Name: "corp", URL: sp("./moved.git"), Registry: reg})
	require.NoError(t, err)
	assert.Equal(t, fileURL(repo), res.After.URL)

	_, err = EditRemote(context.Background(), nil, EditRemoteRequest{Name: "corp", URL: sp("./missing.git"), Registry: reg})
	require.ErrorIs(t, err, refuri.ErrSchemelessPath)
	rem, err := reg.Get("corp")
	require.NoError(t, err)
	assert.Equal(t, fileURL(repo), rem.URL, "a refused edit leaves the remote as it was")
}
