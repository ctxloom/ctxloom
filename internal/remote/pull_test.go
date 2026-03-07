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
		Remote: "alice",
		Path:   "security",
		GitRef: "v1.0.0",
	}
	rem := &Remote{
		Name:    "alice",
		URL:     "https://github.com/alice/scm",
		Version: "v1",
	}
	sha := "abc1234"
	filePath := "scm/v1/bundles/security.yaml"
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
	if !strings.Contains(output, "https://github.com/alice/scm") {
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

func TestDisplaySecurityWarningProfile(t *testing.T) {
	var buf bytes.Buffer

	ref := &Reference{
		Remote: "alice",
		Path:   "secure",
		GitRef: "v1.0.0",
	}
	rem := &Remote{
		Name:    "alice",
		URL:     "https://github.com/alice/scm",
		Version: "v1",
	}
	sha := "abc1234"
	filePath := "scm/v1/profiles/secure.yaml"
	content := []byte("name: secure\nbundles:\n  - alice/security\n")

	secure, _ := ParseSecureContent(ItemTypeProfile, content)
	tc := &mockTerminalChecker{isWriter: false}
	displaySecurityWarning(&buf, ref, rem, sha, filePath, content, secure, tc)

	output := buf.String()

	// Check warning banner is present
	if !strings.Contains(output, "WARNING: PROMPT INJECTION RISK") {
		t.Error("Missing warning banner")
	}

	// Check source info
	if !strings.Contains(output, "https://github.com/alice/scm") {
		t.Error("Missing source URL")
	}

	// Check content markers
	if !strings.Contains(output, "CONTENT START") {
		t.Error("Missing content start marker")
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
	rm, _ := NewReplaceManager("", WithReplaceFS(fs))
	vm := NewVendorManager("/test", WithVendorFS(fs))
	lm := NewLockfileManager("/test", WithLockfileFS(fs))
	tc := &mockTerminalChecker{}
	ff := mockFetcherFactory(newMockFetcher())

	// Create registry
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithReplaceManager(rm),
		WithVendorManager(vm),
		WithLockfileManager(lm),
		WithTerminalChecker(tc),
		WithFetcherFactory(ff),
	)

	assert.NotNil(t, puller)
	assert.Equal(t, fs, puller.fs)
	assert.Equal(t, rm, puller.replaceManager)
	assert.Equal(t, vm, puller.vendorManager)
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	// Create mock fetcher with content
	mf := newMockFetcher()
	mf.files["scm/v1/bundles/security.yaml"] = []byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")
	mf.refs["main"] = "abc123def456"

	// Create lockfile manager
	lm := NewLockfileManager("/test", WithLockfileFS(fs))

	tc := &mockTerminalChecker{isReader: true}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithLockfileManager(lm),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithReplaceManager(nil),
		WithVendorManager(nil),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "alice/security", PullOptions{
		Force:    true,
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader(""),
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "/test/bundles/alice/security.yaml", result.LocalPath)
	assert.Equal(t, "abc123def456", result.SHA)

	// Verify file was written
	exists, err := afero.Exists(fs, result.LocalPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify security warning was displayed
	assert.Contains(t, stdout.String(), "WARNING")
	assert.Contains(t, stdout.String(), "Security bundle")
}

func TestPuller_Pull_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithReplaceManager(nil),
		WithVendorManager(nil),
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
	_ = registry.Add("alice", "https://github.com/alice/scm")

	// Create mock fetcher
	mf := newMockFetcher()
	mf.files["scm/v1/bundles/security.yaml"] = []byte("description: test\n")

	// Terminal checker returns false (not a terminal)
	tc := &mockTerminalChecker{isReader: false}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithReplaceManager(nil),
		WithVendorManager(nil),
	)

	_, err := puller.Pull(context.Background(), "alice/security", PullOptions{
		Force:    false, // Not forcing
		ItemType: ItemTypeBundle,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "interactive terminal required")
}

func TestPuller_Pull_UserCancels(t *testing.T) {
	fs := afero.NewMemMapFs()

	registry, _ := NewRegistry("", WithRegistryFS(fs))
	_ = registry.Add("alice", "https://github.com/alice/scm")

	mf := newMockFetcher()
	mf.files["scm/v1/bundles/security.yaml"] = []byte("description: test\n")

	tc := &mockTerminalChecker{isReader: true}

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithTerminalChecker(tc),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithReplaceManager(nil),
		WithVendorManager(nil),
	)

	var stdout bytes.Buffer
	_, err := puller.Pull(context.Background(), "alice/security", PullOptions{
		Force:    false,
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
		Stdin:    strings.NewReader("n\n"), // User says no
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestPuller_Pull_WithReplaceDirective(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create local replacement file
	require.NoError(t, fs.MkdirAll("/local", 0755))
	require.NoError(t, afero.WriteFile(fs, "/local/security.yaml", []byte("local content\n"), 0644))

	// Create replace manager with directive
	rm, _ := NewReplaceManager("/test", WithReplaceFS(fs))
	_ = rm.Add("alice/security", "/local/security.yaml")

	registry, _ := NewRegistry("", WithRegistryFS(fs))

	puller := NewPuller(registry, AuthConfig{},
		WithPullerFS(fs),
		WithReplaceManager(rm),
		WithVendorManager(nil),
	)

	var stdout bytes.Buffer
	result, err := puller.Pull(context.Background(), "alice/security", PullOptions{
		Force:    true,
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &stdout,
	})

	require.NoError(t, err)
	assert.Equal(t, "/local/security.yaml", result.LocalPath)
	assert.Equal(t, "local", result.SHA)
	assert.Contains(t, stdout.String(), "Using local replace")
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
		fetcher, err := defaultFetcherFactory("https://github.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, fetcher.Forge())
	})

	t.Run("creates GitLab fetcher", func(t *testing.T) {
		fetcher, err := defaultFetcherFactory("https://gitlab.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitLab, fetcher.Forge())
	})
}

func TestCascadePullProfile(t *testing.T) {
	ctx := context.Background()

	t.Run("returns nil for profile with no bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
		)

		profileContent := []byte("description: Test profile\n")
		var stdout bytes.Buffer

		pulled, err := puller.cascadePullProfile(ctx, profileContent, PullOptions{
			Stdout: &stdout,
		})

		require.NoError(t, err)
		assert.Nil(t, pulled)
	})

	t.Run("skips already cached bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Create the cached bundle file
		require.NoError(t, fs.MkdirAll(".scm/bundles/alice", 0755))
		require.NoError(t, afero.WriteFile(fs, ".scm/bundles/alice/security.yaml", []byte("cached"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
		)

		profileContent := []byte("bundles:\n  - alice/security\n")
		var stdout bytes.Buffer

		pulled, err := puller.cascadePullProfile(ctx, profileContent, PullOptions{
			Stdout:   &stdout,
			LocalDir: ".scm",
		})

		require.NoError(t, err)
		assert.Empty(t, pulled) // Nothing was pulled since it was cached
		assert.Contains(t, stdout.String(), "[cached]")
	})

	t.Run("warns on invalid bundle reference", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
		)

		profileContent := []byte("bundles:\n  - invalid-no-slash\n")
		var stdout bytes.Buffer

		pulled, err := puller.cascadePullProfile(ctx, profileContent, PullOptions{
			Stdout: &stdout,
		})

		require.NoError(t, err)
		assert.Empty(t, pulled)
		assert.Contains(t, stdout.String(), "Warning")
	})

	t.Run("pulls uncached bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Setup registry with remote
		require.NoError(t, fs.MkdirAll(".scm", 0755))
		registry, _ := NewRegistry(".scm/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

		// Mock fetcher
		mf := newMockFetcher()
		mf.files["scm/v1/bundles/security.yaml"] = []byte("description: Security bundle\n")
		mf.refs["main"] = "abc123"

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithFetcherFactory(mockFetcherFactory(mf)),
			WithLockfileManager(NewLockfileManager(".scm", WithLockfileFS(fs))),
		)

		profileContent := []byte("bundles:\n  - alice/security\n")
		var stdout bytes.Buffer

		pulled, err := puller.cascadePullProfile(ctx, profileContent, PullOptions{
			Stdout:   &stdout,
			LocalDir: ".scm",
			Force:    true,
		})

		require.NoError(t, err)
		assert.Contains(t, pulled, "alice/security")
		assert.Contains(t, stdout.String(), "Pulling alice/security")
	})

	t.Run("returns error for invalid YAML", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
		)

		profileContent := []byte("invalid: yaml: [[")
		var stdout bytes.Buffer

		_, err := puller.cascadePullProfile(ctx, profileContent, PullOptions{
			Stdout: &stdout,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse profile")
	})
}

func TestTransformProfileContent(t *testing.T) {
	t.Run("returns content unchanged when no bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("description: Test profile\n")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})

	t.Run("returns content unchanged when bundles are already local", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("bundles:\n  - alice/security\n  - bob/testing\n")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})

	t.Run("transforms canonical URLs to local names", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(".scm", 0755))

		registry, _ := NewRegistry(".scm/remotes.yaml", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		// Initialize empty lockfile
		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[string]LockEntry), Profiles: make(map[string]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("bundles:\n  - https://github.com/alice/scm@v1/bundles/security\n")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		// Should have transformed the URL
		assert.Contains(t, stdout.String(), "Transforming canonical URLs")
		// The result should contain a local reference
		assert.NotContains(t, string(result), "https://github.com")
	})

	t.Run("handles invalid YAML gracefully", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("not valid yaml: [[")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		assert.Equal(t, content, result) // Returns unchanged
	})

	t.Run("handles bundles field that is not a list", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("bundles: not-a-list\n")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		assert.Equal(t, content, result) // Returns unchanged
	})

	t.Run("preserves item path suffix during transformation", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(".scm", 0755))

		registry, _ := NewRegistry(".scm/remotes.yaml", WithRegistryFS(fs))
		lm := NewLockfileManager(".scm", WithLockfileFS(fs))

		// Initialize empty lockfile
		require.NoError(t, lm.Save(&Lockfile{Version: 1, Bundles: make(map[string]LockEntry), Profiles: make(map[string]LockEntry)}))

		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithLockfileManager(lm),
		)

		content := []byte("bundles:\n  - https://github.com/alice/scm@v1/bundles/security#fragments/tdd\n")
		var stdout bytes.Buffer

		result, err := puller.transformProfileContent(content, &stdout)

		require.NoError(t, err)
		// Should preserve the #fragments/tdd suffix
		assert.Contains(t, string(result), "#fragments/tdd")
	})
}
