package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/ident"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// mockFetcher is a test double for Fetcher.
type mockFetcher struct {
	files         map[string][]byte
	defaultBranch string
	refs          map[string]string
	forge         ForgeType
	// readErr, when set, is what every FetchFile returns: a read that FAILED,
	// as distinct from a file that is absent.
	readErr error
	// fetched records every path FetchFile was asked for, in order.
	fetched []string
}

func newMockFetcher() *mockFetcher {
	return &mockFetcher{
		files:         make(map[string][]byte),
		defaultBranch: "main",
		refs:          make(map[string]string),
		forge:         ForgeGitHub,
	}
}

func (m *mockFetcher) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	m.fetched = append(m.fetched, path)
	if m.readErr != nil {
		return nil, m.readErr
	}
	if content, ok := m.files[path]; ok {
		return content, nil
	}
	return nil, &fileNotFoundError{path: path}
}

// ListDir lists the direct file children of path in the files map.
func (m *mockFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	var out []DirEntry
	for f := range m.files {
		if rest, ok := strings.CutPrefix(f, path+"/"); ok && !strings.Contains(rest, "/") {
			out = append(out, DirEntry{Name: rest})
		}
	}
	return out, nil
}

func (m *mockFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	if sha, ok := m.refs[ref]; ok {
		return sha, nil
	}
	// Default to returning the ref as-is for testing
	return ref + "000000", nil
}

func (m *mockFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	return nil, nil
}

func (m *mockFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	return true, nil
}

func (m *mockFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	return m.defaultBranch, nil
}

func (m *mockFetcher) Forge() ForgeType {
	return m.forge
}

type fileNotFoundError struct {
	path string
}

func (e *fileNotFoundError) Error() string {
	return "file not found: " + e.path
}

// Unwrap carries the sentinel the production fetchers wrap for an absent file,
// so callers telling "absent" from "broken" see the same answer here.
func (e *fileNotFoundError) Unwrap() error { return errs.ErrRemoteContentNotFound }

// mockFetcherFactory creates a FetcherFactory that returns the given fetcher.
func mockFetcherFactory(f Fetcher) FetcherFactory {
	return func(repoURL string, auth AuthConfig) (Fetcher, error) {
		return f, nil
	}
}

func TestNewPuller_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()
	lm := NewLockfileManager("/test", WithLockfileFS(fs))
	ff := mockFetcherFactory(newMockFetcher())

	// Create registry
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)

	puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithLockfileManager(lm),
		WithFetcherFactory(ff),
	)

	assert.NotNil(t, puller)
	assert.Equal(t, lm, puller.lockfileManager)
}

// TestPuller_Pull records a dependency pin. A pull no longer shows a security
// warning or asks for confirmation — content is gated per item at exposure —
// so a plain (non-forced) pull just records the lockfile entry.
func TestPuller_Pull(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create registry with a remote
	registryPath := "/test/remotes.yaml"
	require.NoError(t, fs.MkdirAll("/test", 0755))
	registry, err := NewRegistry(registryPath, WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Mock fetcher with NO single file at the bundle's path — FetchFile
	// 404s (wrapping errs.ErrRemoteContentNotFound, which MockFetcher does
	// and the package's other, older mockFetcher does not), so fetchItemBytes
	// falls back to probing the directory form via the wired TreeFetchFunc.
	// A tree is the only shape a bundle can be pulled as now.
	mf := NewMockFetcher()
	mf.Refs["main"] = "abc123def456"

	lm := NewLockfileManager("/test", WithLockfileFS(fs))

	puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithLockfileManager(lm),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithTreeFetcher(treeAt(map[string]map[string]TreeFile{
			".ctxloom/content/bundles/v2/security": {
				BundleManifestName: {Data: []byte("description: Security bundle\n")},
			},
		}, nil)),
	)

	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	// A tree bundle IS materialized to disk (the cache), at the reference's
	// own tree path — unlike the old single-file behaviour, where LocalPath
	// was a synthetic "<remote>:name@sha" string.
	ref, rerr := ParseReference("https://github.com/alice/ctxloom@bundles/security")
	require.NoError(t, rerr)
	assert.Equal(t, mustTreePath(t, ref, "/test"), result.LocalPath)
	assert.Equal(t, "abc123def456", result.SHA)
	assert.NotEmpty(t, result.Content)

	// The lockfile is the only on-disk record of the pin.
	lock, lerr := lm.Load()
	require.NoError(t, lerr)
	entry, ok := lock.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/security")
	require.True(t, ok, "lockfile entry should exist")
	assert.Equal(t, "abc123def456", entry.SHA)
}

// TestPuller_Pull_LockfileWriteFailureIsNotSwallowed pins that for a
// bundle the lockfile is the ONLY on-disk record (writePulledContent is a
// synthetic no-op — nothing else is written). installPulledItem demoted a
// failed lockfile write to a printed "Warning:" and returned success anyway,
// so a pull whose sole persistent record failed to write still reported a SHA
// and LocalPath for a pin that does not exist on disk.
func TestPuller_Pull_LockfileWriteFailureIsNotSwallowed(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/test", 0755))

	registry, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(base))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/v2/security"] = []byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")
	mf.refs["main"] = "abc123def456"

	// A read-only fs makes the lockfile write fail deterministically.
	roFS := afero.NewReadOnlyFs(base)
	lm := NewLockfileManager("/test", WithLockfileFS(roFS))

	puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithLockfileManager(lm),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	_, err = puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
	})

	require.Error(t, err, "a pull whose only persistent record failed to write must not report success")
}

// TestPuller_Pull_RejectsEmptyContent pins that a zero-byte remote file
// must not be pulled and pinned as a successful install with no warning — that
// silently reports "installed" for a bundle with nothing in it.
func TestPuller_Pull_RejectsEmptyContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/v2/security"] = []byte{} // zero bytes
	mf.refs["main"] = "abc123"

	lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))
	puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(lm),
	)

	_, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		ItemType: ItemTypeBundle,
	})

	require.Error(t, err, "a zero-byte remote file must not pull successfully")

	// Nothing must have been pinned either.
	lock, lerr := lm.Load()
	require.NoError(t, lerr)
	_, ok := lock.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/security")
	assert.False(t, ok, "an empty fetch must not write a lockfile pin")
}

func TestPuller_Pull_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithTreeInstaller(stubTreeInstaller()))

	_, err := puller.Pull(context.Background(), "invalid", PullOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

func TestDefaultFetcherFactory(t *testing.T) {
	t.Run("creates GitHub fetcher", func(t *testing.T) {
		fetcher, err := DefaultFetcherFactory("https://github.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, fetcher.Forge())
	})

	t.Run("rejects a generic git host (no API fetcher)", func(t *testing.T) {
		_, err := DefaultFetcherFactory("https://gitlab.com/owner/repo", AuthConfig{})
		require.Error(t, err)
	})
}

func TestPuller_UpdateLockfile(t *testing.T) {
	t.Run("records entry in lockfile", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(paths.AppDirName, 0755))

		registry, _ := NewRegistry(paths.DefaultRemotesPath(), WithRegistryFS(fs))
		lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

		// Initialize empty lockfile
		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[ident.BundleKey]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		hadExisting, err := puller.updateLockfile(&fetchedItem{localName: "ctxloom+git://github.com/alice/ctxloom//bundles/security", rem: rem, sha: "abc123def456", requestedVersion: "^1.0", resolvedVersion: "v1.0.0", kind: SelectorVersion}, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, hadExisting, "a brand new entry is not an overwrite")

		// Verify lockfile was updated
		loaded, err := lm.Load()
		require.NoError(t, err)
		entry, ok := loaded.Bundles["ctxloom+git://github.com/alice/ctxloom//bundles/security"]
		assert.True(t, ok)
		assert.Equal(t, "abc123def456", entry.SHA)
		assert.Equal(t, "^1.0", entry.RequestedVersion)
		assert.Equal(t, "v1.0.0", entry.Version, "the resolved semver tag is recorded")
		assert.Equal(t, "https://github.com/alice/ctxloom", entry.URL)
	})

	t.Run("handles multiple entries", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(paths.AppDirName, 0755))

		registry, _ := NewRegistry(paths.DefaultRemotesPath(), WithRegistryFS(fs))
		lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[ident.BundleKey]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		_, err := puller.updateLockfile(&fetchedItem{localName: "ctxloom+git://github.com/alice/ctxloom//bundles/security", rem: rem, sha: "abc123", requestedVersion: "v1.0.0", kind: SelectorVersion}, ItemTypeBundle)
		require.NoError(t, err)

		_, err = puller.updateLockfile(&fetchedItem{localName: "ctxloom+git://github.com/alice/ctxloom//bundles/testing", rem: rem, sha: "def456", requestedVersion: "v2.0.0", kind: SelectorVersion}, ItemTypeBundle)
		require.NoError(t, err)

		loaded, err := lm.Load()
		require.NoError(t, err)
		assert.Len(t, loaded.Bundles, 2)
		assert.Contains(t, loaded.Bundles, ident.BundleKey("ctxloom+git://github.com/alice/ctxloom//bundles/security"))
		assert.Contains(t, loaded.Bundles, ident.BundleKey("ctxloom+git://github.com/alice/ctxloom//bundles/testing"))
	})
}

// A pull never moves an existing pin, held or not: `deps pull --force` repairs
// a tree, it does not advance past the pin, and the hold is carried forward.
func TestPuller_Pull_ARePullKeepsTheHeldPin(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))
	lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

	const ref = "https://github.com/alice/ctxloom@bundles/security"
	seeded := &Lockfile{Version: LockfileVersion, Bundles: map[ident.BundleKey]LockEntry{}}
	seeded.AddEntry(ItemTypeBundle, lockKeyOf(t, ref), LockEntry{
		SHA: "pinnedsha", URL: "https://github.com/alice/ctxloom", Version: "v1.0.0", RequestedVersion: "v1.0.0", Held: true,
	})
	require.NoError(t, lm.Save(seeded))

	mf := NewMockFetcher()
	mf.Refs["main"] = "newhead"
	var checkedOut string
	puller := NewPuller(registry, AuthConfig{},
		WithTreeInstaller(func(_ context.Context, _, sha, subpath, worktreeDir string) (string, error) {
			checkedOut = sha
			return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
		}),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(lm),
		WithTreeFetcher(treeAt(map[string]map[string]TreeFile{
			".ctxloom/content/bundles/v2/security": {BundleManifestName: {Data: []byte("description: Security\n")}},
		}, nil)),
	)

	res, err := puller.Pull(context.Background(), ref, PullOptions{LocalDir: paths.AppDirName, ItemType: ItemTypeBundle})
	require.NoError(t, err)
	assert.Equal(t, "pinnedsha", res.SHA)
	assert.Equal(t, "pinnedsha", checkedOut, "the checkout and the record are one commit: the held one")
	assert.True(t, res.Reinstalled)

	loaded, err := lm.Load()
	require.NoError(t, err)
	entry := loaded.Bundles[lockKeyOf(t, ref)]
	assert.True(t, entry.Held, "a re-pull must not clear the hold")
	assert.Equal(t, "pinnedsha", entry.SHA, "a re-pull must not advance the pin to HEAD")
	assert.Equal(t, "v1.0.0", entry.Version)
}

// A lock entry with no commit is no pin: a pull resolves the ref as it would
// for a first pin, rather than checking out an empty commit.
func TestPuller_Pull_AnEntryWithoutACommitIsResolved(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))
	lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

	const ref = "https://github.com/alice/ctxloom@bundles/security"
	seeded := &Lockfile{Version: LockfileVersion, Bundles: map[ident.BundleKey]LockEntry{}}
	seeded.AddEntry(ItemTypeBundle, lockKeyOf(t, ref), LockEntry{URL: "https://github.com/alice/ctxloom"})
	require.NoError(t, lm.Save(seeded))

	mf := NewMockFetcher()
	mf.Refs["main"] = "newhead"
	var checkedOut string
	puller := NewPuller(registry, AuthConfig{},
		WithTreeInstaller(func(_ context.Context, _, sha, subpath, worktreeDir string) (string, error) {
			checkedOut = sha
			return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
		}),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(lm),
		WithTreeFetcher(treeAt(map[string]map[string]TreeFile{
			".ctxloom/content/bundles/v2/security": {BundleManifestName: {Data: []byte("description: Security\n")}},
		}, nil)),
	)

	res, err := puller.Pull(context.Background(), ref, PullOptions{LocalDir: paths.AppDirName, ItemType: ItemTypeBundle})
	require.NoError(t, err)
	assert.Equal(t, "newhead", res.SHA)
	assert.Equal(t, "newhead", checkedOut)
}
