package remote

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestPromptConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"lowercase yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"mixed case Yes", "Yes\n", true},
		{"n", "n\n", false},
		{"no", "no\n", false},
		{"empty", "\n", false},
		{"other text", "maybe\n", false},
		{"y with spaces", "  y  \n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			reader := strings.NewReader(tt.input)

			got, err := promptConfirmation(&buf, reader, "Test prompt")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tt.expected {
				t.Errorf("promptConfirmation() = %v, want %v", got, tt.expected)
			}

			// Check prompt was written
			if !strings.Contains(buf.String(), "Test prompt") {
				t.Error("prompt not written to output")
			}
			if !strings.Contains(buf.String(), "[y/N]") {
				t.Error("default indicator not in prompt")
			}
		})
	}
}

// mockFetcher is a test double for Fetcher.
type mockFetcher struct {
	files         map[string][]byte
	defaultBranch string
	refs          map[string]string
	forge         ForgeType
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
	if content, ok := m.files[path]; ok {
		return content, nil
	}
	return nil, &fileNotFoundError{path: path}
}

func (m *mockFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	return nil, nil
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

	puller := NewPuller(registry, AuthConfig{},
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

	// Create mock fetcher with content
	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/security.yaml"] = []byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")
	mf.refs["main"] = "abc123def456"

	lm := NewLockfileManager("/test", WithLockfileFS(fs))

	puller := NewPuller(registry, AuthConfig{},
		WithLockfileManager(lm),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""),
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	// Bundles no longer materialize on fs: LocalPath is a synthetic
	// "<remote>:name@sha" string and the fetched bytes come back on Content.
	assert.Equal(t, "<remote>:https://github.com/alice/ctxloom@bundles/security@abc123def456", result.LocalPath)
	assert.Equal(t, "abc123def456", result.SHA)
	assert.NotEmpty(t, result.Content)

	// The lockfile is the only on-disk record of the pin.
	lock, lerr := lm.Load()
	require.NoError(t, lerr)
	entry, ok := lock.GetEntry(ItemTypeBundle, "https://github.com/alice/ctxloom@bundles/security")
	require.True(t, ok, "lockfile entry should exist")
	assert.Equal(t, "abc123def456", entry.SHA)
}

// TestPuller_Pull_LockfileWriteFailureIsNotSwallowed pins that for a
// bundle the lockfile is the ONLY on-disk record (writePulledContent is a
// synthetic no-op — nothing else is written). installPulledItem demoted a
// failed lockfile write to a printed "Warning:" on opts.Stdout and returned
// success anyway, so a pull whose sole persistent record failed to write
// still reported a SHA and LocalPath for a pin that does not exist on disk —
// including, on a retracted item, silently dropping the freshly-computed
// Retracted verdict so EffectiveTrust never learns to withhold it.
func TestPuller_Pull_LockfileWriteFailureIsNotSwallowed(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/test", 0755))

	registry, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(base))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/security.yaml"] = []byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")
	mf.refs["main"] = "abc123def456"

	// A read-only fs makes the lockfile write fail deterministically.
	roFS := afero.NewReadOnlyFs(base)
	lm := NewLockfileManager("/test", WithLockfileFS(roFS))

	puller := NewPuller(registry, AuthConfig{},
		WithLockfileManager(lm),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	var stdout bytes.Buffer
	_, err = puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""),
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
	mf.files[".ctxloom/content/bundles/security.yaml"] = []byte{} // zero bytes
	mf.refs["main"] = "abc123"

	lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))
	puller := NewPuller(registry, AuthConfig{},
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(lm),
	)

	var stdout bytes.Buffer
	_, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""),
	})

	require.Error(t, err, "a zero-byte remote file must not pull successfully")

	// Nothing must have been pinned either.
	lock, lerr := lm.Load()
	require.NoError(t, lerr)
	_, ok := lock.GetEntry(ItemTypeBundle, "https://github.com/alice/ctxloom@bundles/security")
	assert.False(t, ok, "an empty fetch must not write a lockfile pin")
}

func TestPuller_Pull_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	puller := NewPuller(registry, AuthConfig{})

	_, err := puller.Pull(context.Background(), "invalid", PullOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

// TestPuller_Pull_RetractedVersion_Force pins the retraction contract: a
// retracted version warns, and a forced (non-interactive) pull proceeds without
// prompting — sync/batch callers set Force so a prompt never blocks on a stdin
// nobody answers.
func TestPuller_Pull_RetractedVersion_Force(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/security.yaml"] = []byte("description: Security\n")
	mf.files[".ctxloom/content/manifest.yaml"] = []byte(`retracted:
  - type: bundle
    name: security
    reason: compromised release
`)
	mf.refs["main"] = "abc123"

	puller := NewPuller(registry, AuthConfig{},
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Force:    true,
		LocalDir: paths.AppDirName,
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""), // EOF: a prompt read here would fail the pull
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, stdout.String(), "retracted")
}

func TestPuller_Pull_NoStdoutStdin(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Mock fetcher
	mf := newMockFetcher()
	mf.files[".ctxloom/content/bundles/security.yaml"] = []byte("description: Security\n")
	mf.refs["main"] = "abc123"

	puller := NewPuller(registry, AuthConfig{},
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))),
	)

	// Call with nil Stdout and Stdin - should use defaults
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Force:    true,
		LocalDir: paths.AppDirName,
		ItemType: ItemTypeBundle,
		Stdout:   nil, // Should default to os.Stdout
		Stdin:    nil, // Should default to os.Stdin
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "abc123", result.SHA)
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
		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[string]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{},
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		hadExisting, err := puller.updateLockfile("https://github.com/alice/ctxloom@bundles/security", PullOptions{ItemType: ItemTypeBundle}, rem, "abc123def456", "^1.0", "v1.0.0", SelectorVersion, false, "", time.Time{}, false)

		require.NoError(t, err)
		assert.False(t, hadExisting, "a brand new entry is not an overwrite")

		// Verify lockfile was updated
		loaded, err := lm.Load()
		require.NoError(t, err)
		entry, ok := loaded.Bundles["https://github.com/alice/ctxloom@bundles/security"]
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

		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[string]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{},
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		_, err := puller.updateLockfile("https://github.com/alice/ctxloom@bundles/security", PullOptions{ItemType: ItemTypeBundle}, rem, "abc123", "v1.0.0", "", SelectorVersion, false, "", time.Time{}, false)
		require.NoError(t, err)

		_, err = puller.updateLockfile("alice/testing", PullOptions{ItemType: ItemTypeBundle}, rem, "def456", "v2.0.0", "", SelectorVersion, false, "", time.Time{}, false)
		require.NoError(t, err)

		loaded, err := lm.Load()
		require.NoError(t, err)
		assert.Len(t, loaded.Bundles, 2)
		assert.Contains(t, loaded.Bundles, "https://github.com/alice/ctxloom@bundles/security")
		assert.Contains(t, loaded.Bundles, "alice/testing")
	})

	// A blanket re-pull (no explicit version, as in `deps pull --force`) must
	// not silently un-hold a held entry or advance its frozen SHA. The hold is a
	// "do not upgrade" decision; force repairs, it does not move past the hold.
	t.Run("blanket re-pull preserves hold and frozen SHA", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(paths.AppDirName, 0755))

		registry, _ := NewRegistry(paths.DefaultRemotesPath(), WithRegistryFS(fs))
		lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

		const ref = "https://github.com/alice/ctxloom@bundles/security"
		seeded := &Lockfile{Version: 1, Bundles: make(map[string]LockEntry)}
		seeded.AddEntry(ItemTypeBundle, ref, LockEntry{
			SHA: "pinnedsha", URL: "https://github.com/alice/ctxloom",
			Version: "v1.0.0", RequestedVersion: "v1.0.0", Pinned: true,
		})
		require.NoError(t, lm.Save(seeded))

		puller := NewPuller(registry, AuthConfig{}, WithLockfileManager(lm))
		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		// Force pull resolves default-branch HEAD ("newhead") with no requested version.
		requireUpdateLockfile(t, puller, ref, "newhead", "", rem)

		loaded, err := lm.Load()
		require.NoError(t, err)
		entry := loaded.Bundles[ref]
		assert.True(t, entry.Pinned, "hold must survive a blanket re-pull")
		assert.Equal(t, "pinnedsha", entry.SHA, "frozen SHA must not advance to HEAD")
		assert.Equal(t, "v1.0.0", entry.Version)
	})

	// An explicit version pull is a deliberate move; it advances a held entry
	// but keeps it held at the new SHA (the flag is never silently dropped).
	t.Run("explicit version pull advances a held entry but keeps the hold", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(paths.AppDirName, 0755))

		registry, _ := NewRegistry(paths.DefaultRemotesPath(), WithRegistryFS(fs))
		lm := NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))

		const ref = "https://github.com/alice/ctxloom@bundles/security"
		seeded := &Lockfile{Version: 1, Bundles: make(map[string]LockEntry)}
		seeded.AddEntry(ItemTypeBundle, ref, LockEntry{
			SHA: "pinnedsha", URL: "https://github.com/alice/ctxloom", Pinned: true,
		})
		require.NoError(t, lm.Save(seeded))

		puller := NewPuller(registry, AuthConfig{}, WithLockfileManager(lm))
		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		requireUpdateLockfile(t, puller, ref, "v2sha", "v2.0.0", rem)

		loaded, err := lm.Load()
		require.NoError(t, err)
		entry := loaded.Bundles[ref]
		assert.True(t, entry.Pinned, "hold must survive an explicit move")
		assert.Equal(t, "v2sha", entry.SHA, "explicit version pull advances the SHA")
		assert.Equal(t, "v2.0.0", entry.RequestedVersion)
	})
}

// requireUpdateLockfile calls updateLockfile with a bundle PullOptions and
// fails the test on error (helper for the hold-preservation cases above).
func requireUpdateLockfile(t *testing.T, puller *Puller, ref, sha, requestedVersion string, rem *Remote) {
	t.Helper()
	_, err := puller.updateLockfile(ref, PullOptions{ItemType: ItemTypeBundle}, rem, sha, requestedVersion, "", "", false, "", time.Time{}, false)
	require.NoError(t, err)
}
