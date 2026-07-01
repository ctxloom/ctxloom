package remote

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// mockTerminalChecker is a test double for TerminalChecker.
type mockTerminalChecker struct {
	isReader bool
	isWriter bool
}

func (m *mockTerminalChecker) IsTerminalReader(r io.Reader) bool {
	return m.isReader
}

func (m *mockTerminalChecker) IsTerminalWriter(w io.Writer) bool {
	return m.isWriter
}

func TestDisplaySecurityWarning(t *testing.T) {
	var buf bytes.Buffer

	ref := &Reference{
		URL:            "https://github.com/alice/ctxloom",
		ItemType:       ItemTypeBundle,
		Path:           "security",
		ContentVersion: "v1.0.0",
	}
	rem := &Remote{
		Name: "alice",
		URL:  "https://github.com/alice/ctxloom",
	}
	sha := "abc1234"
	filePath := "ctxloom/bundles/security.yaml"
	content := []byte("description: Test bundle\nfragments:\n  tdd:\n    content: Test content here\n")

	secure, _ := ParseSecureContent(ItemTypeBundle, content)
	tc := &mockTerminalChecker{isWriter: false}
	displaySecurityWarning(&buf, ref, rem, sha, filePath, content, secure, tc)

	output := buf.String()

	// Check warning banner is present (bundles show "BUNDLE INSTALLATION")
	if !strings.Contains(output, "WARNING: BUNDLE INSTALLATION") {
		t.Error("Missing warning banner")
	}

	// Check source info
	if !strings.Contains(output, "https://github.com/alice/ctxloom") {
		t.Error("Missing source URL")
	}
	if !strings.Contains(output, "abc1234") {
		t.Error("Missing SHA")
	}
	if !strings.Contains(output, "alice") {
		t.Error("Missing org")
	}
	if !strings.Contains(output, "security") {
		t.Error("Missing name")
	}

	// Check content markers
	if !strings.Contains(output, "CONTENT START") {
		t.Error("Missing content start marker")
	}
	if !strings.Contains(output, "CONTENT END") {
		t.Error("Missing content end marker")
	}

	// Check content is present
	if !strings.Contains(output, "Test content here") {
		t.Error("Missing content body")
	}
}

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
	tc := &mockTerminalChecker{}
	ff := mockFetcherFactory(newMockFetcher())

	// Create registry
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithLockfileManager(lm),
		WithTerminalChecker(tc),
		WithFetcherFactory(ff),
	)

	assert.NotNil(t, puller)
	assert.Equal(t, fs, puller.fs)
	assert.Equal(t, lm, puller.lockfileManager)
	assert.Equal(t, tc, puller.terminalChecker)
}

func TestPuller_Pull_Force(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create registry with a remote
	registryPath := "/test/remotes.yaml"
	require.NoError(t, fs.MkdirAll("/test", 0755))
	registry, err := NewRegistry(registryPath, WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Create mock fetcher with content
	mf := newMockFetcher()
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")
	mf.refs["main"] = "abc123def456"

	// Create lockfile manager
	lm := NewLockfileManager("/test", WithLockfileFS(fs))

	tc := &mockTerminalChecker{isReader: true}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithLockfileManager(lm),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Force:    true,
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""),
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	// Bundles no longer materialize on fs after PR 1 of the bundle-review
	// plan. LocalPath is a synthetic "<remote>:name@sha" string and the
	// fetched bytes come back on Content for callers that need them.
	assert.Equal(t, "<remote>:https://github.com/alice/ctxloom@bundles/security@abc123def456", result.LocalPath)
	assert.Equal(t, "abc123def456", result.SHA)
	assert.NotEmpty(t, result.Content)

	// The lockfile is now the only on-disk record of the install.
	lock, lerr := lm.Load()
	require.NoError(t, lerr)
	entry, ok := lock.GetEntry(ItemTypeBundle, "https://github.com/alice/ctxloom@bundles/security")
	require.True(t, ok, "lockfile entry should exist")
	assert.Equal(t, "abc123def456", entry.SHA)

	// Verify security warning was displayed
	assert.Contains(t, stdout.String(), "WARNING")
	assert.Contains(t, stdout.String(), "Security bundle")
}

func TestPuller_Pull_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
	)

	_, err := puller.Pull(context.Background(), "invalid", PullOptions{
		Force: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

func TestPuller_Pull_RequiresTerminalWithoutForce(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create registry with a remote
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	_ = registry.Add("alice", "https://github.com/alice/ctxloom")

	// Create mock fetcher
	mf := newMockFetcher()
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: test\n")

	// Terminal checker returns false (not a terminal)
	tc := &mockTerminalChecker{isReader: false}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	_, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Force:    false, // Not forcing
		ItemType: ItemTypeBundle,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "interactive terminal required")
}

func TestPuller_Pull_UserCancels(t *testing.T) {
	fs := afero.NewMemMapFs()

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	_ = registry.Add("alice", "https://github.com/alice/ctxloom")

	mf := newMockFetcher()
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: test\n")

	tc := &mockTerminalChecker{isReader: true}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
	)

	var stdout bytes.Buffer
	_, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Force:    false,
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader("n\n"), // User says no
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestPuller_Pull_BlindMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Mock fetcher
	mf := newMockFetcher()
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: Security\n")
	mf.refs["main"] = "abc123"

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))),
		WithTerminalChecker(&mockTerminalChecker{isReader: false}),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Blind:    true,
		LocalDir: paths.AppDirName,
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, stdout.String(), "Blind mode")
}

// TestPuller_Pull_BlindMode_RetractedVersion_DoesNotPrompt pins the Blind
// contract: Blind implies Force for every confirmation gate, including the
// retraction prompt. Blind pulls run non-interactively (MCP startup sync), so
// any prompt would block on a stdin nobody answers.
func TestPuller_Pull_BlindMode_RetractedVersion_DoesNotPrompt(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mf := newMockFetcher()
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: Security\n")
	mf.files["ctxloom/manifest.yaml"] = []byte(`retracted:
  - type: bundle
    name: security
    reason: compromised release
`)
	mf.refs["main"] = "abc123"

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithLockfileManager(NewLockfileManager(paths.AppDirName, WithLockfileFS(fs))),
		WithTerminalChecker(&mockTerminalChecker{isReader: false}),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "https://github.com/alice/ctxloom@bundles/security", PullOptions{
		Blind:    true,
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
	mf.files["ctxloom/bundles/security.yaml"] = []byte("description: Security\n")
	mf.refs["main"] = "abc123"

	tc := &mockTerminalChecker{isReader: true, isWriter: true}
	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithTerminalChecker(tc),
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

func TestDefaultTerminalChecker(t *testing.T) {
	checker := &defaultTerminalChecker{}

	t.Run("IsTerminalReader returns false for bytes.Buffer", func(t *testing.T) {
		buf := &bytes.Buffer{}
		assert.False(t, checker.IsTerminalReader(buf))
	})

	t.Run("IsTerminalReader returns false for strings.Reader", func(t *testing.T) {
		reader := strings.NewReader("test")
		assert.False(t, checker.IsTerminalReader(reader))
	})

	t.Run("IsTerminalWriter returns false for bytes.Buffer", func(t *testing.T) {
		buf := &bytes.Buffer{}
		assert.False(t, checker.IsTerminalWriter(buf))
	})
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
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		_, err := puller.updateLockfile("https://github.com/alice/ctxloom@bundles/security", PullOptions{ItemType: ItemTypeBundle}, rem, "abc123def456", "^1.0", "v1.0.0", SelectorVersion)

		require.NoError(t, err)

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
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		_, err := puller.updateLockfile("https://github.com/alice/ctxloom@bundles/security", PullOptions{ItemType: ItemTypeBundle}, rem, "abc123", "v1.0.0", "", SelectorVersion)
		require.NoError(t, err)

		_, err = puller.updateLockfile("alice/testing", PullOptions{ItemType: ItemTypeBundle}, rem, "def456", "v2.0.0", "", SelectorVersion)
		require.NoError(t, err)

		loaded, err := lm.Load()
		require.NoError(t, err)
		assert.Len(t, loaded.Bundles, 2)
		assert.Contains(t, loaded.Bundles, "https://github.com/alice/ctxloom@bundles/security")
		assert.Contains(t, loaded.Bundles, "alice/testing")
	})

	// A blanket re-pull (no explicit version, as in `remote pull --force`) must
	// not silently un-pin a pinned entry or advance its frozen SHA. The pin is a
	// "do not upgrade" decision; force repairs, it does not move past the pin.
	t.Run("blanket re-pull preserves pin and frozen SHA on the active lockfile", func(t *testing.T) {
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

		puller := NewPuller(registry, AuthConfig{}, WithPullerFS(fs), WithLockfileManager(lm))
		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		// Force pull resolves default-branch HEAD ("newhead") with no requested version.
		requireUpdateLockfile(t, puller, ref, "newhead", "", rem)

		loaded, err := lm.Load()
		require.NoError(t, err)
		entry := loaded.Bundles[ref]
		assert.True(t, entry.Pinned, "pin must survive a blanket re-pull")
		assert.Equal(t, "pinnedsha", entry.SHA, "frozen SHA must not advance to HEAD")
		assert.Equal(t, "v1.0.0", entry.Version)
	})

	// An explicit version pull is a deliberate move; it advances a pinned entry
	// but keeps it pinned at the new SHA (the flag is never silently dropped).
	t.Run("explicit version pull advances a pinned entry but keeps the pin", func(t *testing.T) {
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

		puller := NewPuller(registry, AuthConfig{}, WithPullerFS(fs), WithLockfileManager(lm))
		rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

		requireUpdateLockfile(t, puller, ref, "v2sha", "v2.0.0", rem)

		loaded, err := lm.Load()
		require.NoError(t, err)
		entry := loaded.Bundles[ref]
		assert.True(t, entry.Pinned, "pin must survive an explicit move")
		assert.Equal(t, "v2sha", entry.SHA, "explicit version pull advances the SHA")
		assert.Equal(t, "v2.0.0", entry.RequestedVersion)
	})
}

// requireUpdateLockfile calls updateLockfile with a bundle PullOptions and
// fails the test on error (helper for the pin-preservation cases above).
func requireUpdateLockfile(t *testing.T, puller *Puller, ref, sha, requestedVersion string, rem *Remote) {
	t.Helper()
	_, err := puller.updateLockfile(ref, PullOptions{ItemType: ItemTypeBundle}, rem, sha, requestedVersion, "", "")
	require.NoError(t, err)
}
