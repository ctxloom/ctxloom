package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsURLReference(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://github.com/owner/repo@v1/bundles/name", true},
		{"http://github.com/owner/repo@v1/bundles/name", true},
		{"git@github.com:owner/repo@v1/bundles/name", true},
		{"file:///path/to/repo@v1/bundles/name", true},
		{"local-bundle", false},
		{"remote/bundle", false},
		{"alice/security", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := IsURLReference(tt.ref); got != tt.want {
				t.Errorf("IsURLReference(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestExtractBundleName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{".ctxloom/bundles/github.com/owner/repo/core-practices.yaml", "core-practices"},
		{"/home/user/.ctxloom/bundles/testing.yaml", "testing"},
		{"bundle.yaml", "bundle"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := ExtractBundleName(tt.path); got != tt.want {
				t.Errorf("ExtractBundleName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBundleResolver_ResolveToLocalPath(t *testing.T) {
	// Create temp directory with a bundle file
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundleDir := filepath.Join(appDir, "bundles", "github.com", "owner", "repo")
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(bundleDir, "core-practices.yaml")
	if err := os.WriteFile(bundlePath, []byte("version: '1.0.0'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := NewBundleResolver(appDir)

	tests := []struct {
		name      string
		bundleRef string
		wantPath  string
		wantErr   bool
	}{
		{
			name:      "valid https reference",
			bundleRef: "https://github.com/owner/repo@v1/bundles/core-practices",
			wantPath:  bundlePath,
			wantErr:   false,
		},
		{
			name:      "bundle not found",
			bundleRef: "https://github.com/owner/repo@v1/bundles/nonexistent",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ResolveToLocalPath(tt.bundleRef)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveToLocalPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantPath {
				t.Errorf("ResolveToLocalPath() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestNewBundleResolver_DefaultDir(t *testing.T) {
	resolver := NewBundleResolver("")
	assert.Equal(t, ".ctxloom", resolver.appDir)
}

func TestNewBundleResolver_WithResolverFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	resolver := NewBundleResolver("/test", WithResolverFS(fs))
	assert.Equal(t, "/test", resolver.appDir)
	assert.NotNil(t, resolver.fs)
}

func TestBundleResolver_WithRemoteConfig(t *testing.T) {
	resolver := NewBundleResolver("/test")
	cfg := &RemoteConfig{}

	result := resolver.WithRemoteConfig(cfg)

	assert.Same(t, resolver, result)
	assert.Same(t, cfg, resolver.remoteConfig)
}

func TestBundleResolver_ResolveToLocalPath_InvalidReference(t *testing.T) {
	resolver := NewBundleResolver("/test")

	_, err := resolver.ResolveToLocalPath("invalid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle reference")
}

func TestBundleResolver_ResolveToLocalPath_WithMemFS(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create bundle file in memory filesystem
	bundlePath := "/test/bundles/github.com/owner/repo/core-practices.yaml"
	require.NoError(t, fs.MkdirAll(filepath.Dir(bundlePath), 0755))
	require.NoError(t, afero.WriteFile(fs, bundlePath, []byte("version: '1.0.0'\n"), 0644))

	resolver := NewBundleResolver("/test", WithResolverFS(fs))

	path, err := resolver.ResolveToLocalPath("https://github.com/owner/repo@v1/bundles/core-practices")
	require.NoError(t, err)
	assert.Equal(t, bundlePath, path)
}

func TestBundleResolver_ResolveBundle(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create bundle file
	bundlePath := "/test/bundles/github.com/owner/repo/core-practices.yaml"
	require.NoError(t, fs.MkdirAll(filepath.Dir(bundlePath), 0755))
	require.NoError(t, afero.WriteFile(fs, bundlePath, []byte("version: '1.0.0'\n"), 0644))

	resolver := NewBundleResolver("/test", WithResolverFS(fs))

	t.Run("resolves canonical reference", func(t *testing.T) {
		result, err := resolver.ResolveBundle("https://github.com/owner/repo@v1/bundles/core-practices")
		require.NoError(t, err)

		assert.Equal(t, "https://github.com/owner/repo@v1/bundles/core-practices", result.OriginalRef)
		assert.Equal(t, bundlePath, result.LocalPath)
		assert.Equal(t, "https://github.com/owner/repo@v1/bundles/core-practices", result.CanonicalURL)
		// LockEntry may be nil if no lockfile exists
	})

	t.Run("returns error for missing bundle", func(t *testing.T) {
		_, err := resolver.ResolveBundle("https://github.com/owner/repo@v1/bundles/nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not cached locally")
	})

	t.Run("returns error for invalid reference", func(t *testing.T) {
		_, err := resolver.ResolveBundle("invalid")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid bundle reference")
	})
}

func TestBundleResolver_ResolveBundle_AliasRef(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create bundle file for alias reference
	bundlePath := "/test/bundles/alice/core-practices.yaml"
	require.NoError(t, fs.MkdirAll(filepath.Dir(bundlePath), 0755))
	require.NoError(t, afero.WriteFile(fs, bundlePath, []byte("version: '1.0.0'\n"), 0644))

	resolver := NewBundleResolver("/test", WithResolverFS(fs))

	result, err := resolver.ResolveBundle("alice/core-practices")
	require.NoError(t, err)

	assert.Equal(t, "alice/core-practices", result.OriginalRef)
	assert.Equal(t, bundlePath, result.LocalPath)
	// For alias refs, canonical string format
	assert.Equal(t, "alice/core-practices", result.CanonicalURL)
}

func TestBundleResolver_LocalPathToCanonicalURL(t *testing.T) {
	// Use real temp directory for integration with lockfile manager
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	// Create lockfile with entry
	lockMgr := NewLockfileManager(appDir)
	lockfile := &Lockfile{
		Version: 1,
		Bundles: map[string]LockEntry{
			"https://github.com/owner/repo@v1/bundles/core-practices": {
				SHA:        "abc123",
				URL:        "https://github.com/owner/repo",
				CtxloomVersion: "v1",
			},
		},
		Profiles: make(map[string]LockEntry),
	}
	require.NoError(t, lockMgr.Save(lockfile))

	resolver := NewBundleResolver(appDir)

	t.Run("finds canonical URL for local path", func(t *testing.T) {
		localPath := filepath.Join(appDir, "bundles/github.com/owner/repo/core-practices.yaml")
		url, err := resolver.LocalPathToCanonicalURL(localPath)
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/owner/repo@v1/bundles/core-practices", url)
	})

	t.Run("returns error for unknown path", func(t *testing.T) {
		localPath := filepath.Join(appDir, "bundles/unknown/bundle.yaml")
		_, err := resolver.LocalPathToCanonicalURL(localPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no lockfile entry found")
	})
}

func TestBundleResolver_LocalPathToCanonicalURL_AliasEntry(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	// Create lockfile with alias entry
	lockMgr := NewLockfileManager(appDir)
	lockfile := &Lockfile{
		Version: 1,
		Bundles: map[string]LockEntry{
			"alice/core-practices": {
				SHA:        "abc123",
				URL:        "https://github.com/alice/ctxloom",
				CtxloomVersion: "v1",
			},
		},
		Profiles: make(map[string]LockEntry),
	}
	require.NoError(t, lockMgr.Save(lockfile))

	resolver := NewBundleResolver(appDir)

	localPath := filepath.Join(appDir, "bundles/alice/core-practices.yaml")
	url, err := resolver.LocalPathToCanonicalURL(localPath)
	require.NoError(t, err)
	// For alias refs, builds canonical URL from entry metadata
	assert.Equal(t, "https://github.com/alice/ctxloom@v1/bundles/core-practices", url)
}
