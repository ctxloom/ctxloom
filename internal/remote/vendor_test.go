package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVendorManager_VendorDir(t *testing.T) {
	manager := NewVendorManager(".scm")
	expected := filepath.Join(".scm", "vendor")

	if got := manager.VendorDir(); got != expected {
		t.Errorf("VendorDir() = %q, want %q", got, expected)
	}
}

func TestVendorManager_DefaultBaseDir(t *testing.T) {
	manager := NewVendorManager("")
	expected := filepath.Join(".scm", "vendor")

	if got := manager.VendorDir(); got != expected {
		t.Errorf("VendorDir() = %q, want %q", got, expected)
	}
}

func TestVendorManager_HasVendored(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewVendorManager(tmpDir)

	ref := &Reference{
		Remote: "alice",
		Path:   "security",
	}

	// Initially not vendored
	if manager.HasVendored(ItemTypeBundle, ref) {
		t.Error("expected HasVendored to return false initially")
	}

	// Create vendored file
	vendorPath := filepath.Join(manager.VendorDir(), "bundles", "alice", "security.yaml")
	if err := os.MkdirAll(filepath.Dir(vendorPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vendorPath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Now should be vendored
	if !manager.HasVendored(ItemTypeBundle, ref) {
		t.Error("expected HasVendored to return true after creating file")
	}
}

func TestVendorManager_GetVendored(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewVendorManager(tmpDir)

	ref := &Reference{
		Remote: "alice",
		Path:   "security",
	}

	content := []byte("vendored content")

	// Create vendored file
	vendorPath := filepath.Join(manager.VendorDir(), "bundles", "alice", "security.yaml")
	_ = os.MkdirAll(filepath.Dir(vendorPath), 0755)
	_ = os.WriteFile(vendorPath, content, 0644)

	// Get vendored content
	got, err := manager.GetVendored(ItemTypeBundle, ref)
	if err != nil {
		t.Fatalf("GetVendored failed: %v", err)
	}

	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", string(got), string(content))
	}

	// Get non-existent
	badRef := &Reference{Remote: "bob", Path: "other"}
	_, err = manager.GetVendored(ItemTypeBundle, badRef)
	if err == nil {
		t.Error("expected error getting non-vendored file")
	}
}

func TestVendorManager_NestedPath(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewVendorManager(tmpDir)

	ref := &Reference{
		Remote: "alice",
		Path:   "lang/go/testing",
	}

	content := []byte("nested vendored content")

	// Create vendored file with nested path
	vendorPath := filepath.Join(manager.VendorDir(), "bundles", "alice", "lang", "go", "testing.yaml")
	_ = os.MkdirAll(filepath.Dir(vendorPath), 0755)
	_ = os.WriteFile(vendorPath, content, 0644)

	if !manager.HasVendored(ItemTypeBundle, ref) {
		t.Error("expected HasVendored to return true for nested path")
	}

	got, err := manager.GetVendored(ItemTypeBundle, ref)
	if err != nil {
		t.Fatalf("GetVendored failed: %v", err)
	}

	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", string(got), string(content))
	}
}

func TestVendorManager_DifferentItemTypes(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewVendorManager(tmpDir)

	ref := &Reference{
		Remote: "alice",
		Path:   "test",
	}

	// Create files for different types (bundles and profiles only)
	for _, itemType := range []ItemType{ItemTypeBundle, ItemTypeProfile} {
		vendorPath := filepath.Join(manager.VendorDir(), itemType.DirName(), "alice", "test.yaml")
		_ = os.MkdirAll(filepath.Dir(vendorPath), 0755)
		_ = os.WriteFile(vendorPath, []byte(string(itemType)), 0644)
	}

	// Verify each type is found correctly
	for _, itemType := range []ItemType{ItemTypeBundle, ItemTypeProfile} {
		if !manager.HasVendored(itemType, ref) {
			t.Errorf("expected HasVendored to return true for %s", itemType)
		}

		content, err := manager.GetVendored(itemType, ref)
		if err != nil {
			t.Errorf("GetVendored(%s) failed: %v", itemType, err)
		}

		if string(content) != string(itemType) {
			t.Errorf("content for %s = %q, want %q", itemType, string(content), string(itemType))
		}
	}
}

func TestVendorManager_IsVendored(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewVendorManager(".scm", WithVendorFS(fs))

	t.Run("returns false when no config exists", func(t *testing.T) {
		assert.False(t, manager.IsVendored())
	})

	t.Run("returns false when vendor not set", func(t *testing.T) {
		require.NoError(t, fs.MkdirAll(".scm", 0755))
		require.NoError(t, afero.WriteFile(fs, ".scm/remotes.yaml", []byte("remotes: {}\n"), 0644))

		assert.False(t, manager.IsVendored())
	})

	t.Run("returns true when vendor enabled", func(t *testing.T) {
		require.NoError(t, afero.WriteFile(fs, ".scm/remotes.yaml", []byte("vendor: true\n"), 0644))

		assert.True(t, manager.IsVendored())
	})

	t.Run("returns false for invalid YAML", func(t *testing.T) {
		require.NoError(t, afero.WriteFile(fs, ".scm/remotes.yaml", []byte("invalid: yaml: [["), 0644))

		assert.False(t, manager.IsVendored())
	})
}

func TestVendorManager_SetVendorMode(t *testing.T) {
	t.Run("enables vendor mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		manager := NewVendorManager(".scm", WithVendorFS(fs))

		err := manager.SetVendorMode(true)
		require.NoError(t, err)

		content, err := afero.ReadFile(fs, ".scm/remotes.yaml")
		require.NoError(t, err)
		assert.Contains(t, string(content), "vendor: true")
	})

	t.Run("disables vendor mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(".scm", 0755))
		require.NoError(t, afero.WriteFile(fs, ".scm/remotes.yaml", []byte("vendor: true\nremotes: {}\n"), 0644))

		manager := NewVendorManager(".scm", WithVendorFS(fs))

		err := manager.SetVendorMode(false)
		require.NoError(t, err)

		content, err := afero.ReadFile(fs, ".scm/remotes.yaml")
		require.NoError(t, err)
		assert.NotContains(t, string(content), "vendor")
	})

	t.Run("preserves existing config", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(".scm", 0755))
		require.NoError(t, afero.WriteFile(fs, ".scm/remotes.yaml", []byte("remotes:\n  alice:\n    url: https://github.com/alice/scm\n"), 0644))

		manager := NewVendorManager(".scm", WithVendorFS(fs))

		err := manager.SetVendorMode(true)
		require.NoError(t, err)

		content, err := afero.ReadFile(fs, ".scm/remotes.yaml")
		require.NoError(t, err)
		assert.Contains(t, string(content), "vendor: true")
		assert.Contains(t, string(content), "remotes")
	})
}

func TestVendorManager_VendorAll(t *testing.T) {
	ctx := context.Background()

	t.Run("vendors all lockfile entries", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Create registry with remote
		require.NoError(t, fs.MkdirAll("/test", 0755))
		registry, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, err)
		require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

		// Create mock fetcher
		mf := newMockFetcher()
		mf.files["scm/v1/bundles/security.yaml"] = []byte("description: Security bundle\n")

		manager := NewVendorManager("/test",
			WithVendorFS(fs),
			WithVendorFetcherFactory(mockFetcherFactory(mf)),
		)

		// Create lockfile
		lockfile := &Lockfile{
			Version: 1,
			Bundles: map[string]LockEntry{
				"alice/security": {
					SHA:        "abc123",
					URL:        "https://github.com/alice/scm",
					SCMVersion: "v1",
				},
			},
			Profiles: make(map[string]LockEntry),
		}

		err = manager.VendorAll(ctx, lockfile, registry, AuthConfig{})
		require.NoError(t, err)

		// Verify vendored file exists
		vendorPath := filepath.Join("/test", "vendor", "bundles", "alice", "security.yaml")
		exists, err := afero.Exists(fs, vendorPath)
		require.NoError(t, err)
		assert.True(t, exists)

		// Verify content
		content, err := afero.ReadFile(fs, vendorPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), "Security bundle")
	})

	t.Run("returns error for empty lockfile", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		manager := NewVendorManager("/test", WithVendorFS(fs))

		lockfile := &Lockfile{
			Version:  1,
			Bundles:  make(map[string]LockEntry),
			Profiles: make(map[string]LockEntry),
		}

		err := manager.VendorAll(ctx, lockfile, registry, AuthConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no entries")
	})

	t.Run("returns error for unknown remote", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		manager := NewVendorManager("/test", WithVendorFS(fs))

		lockfile := &Lockfile{
			Version: 1,
			Bundles: map[string]LockEntry{
				"unknown/security": {SHA: "abc123"},
			},
			Profiles: make(map[string]LockEntry),
		}

		err := manager.VendorAll(ctx, lockfile, registry, AuthConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote not found")
	})
}

func TestNewVendorManager_WithFetcherFactory(t *testing.T) {
	mf := newMockFetcher()
	ff := mockFetcherFactory(mf)

	manager := NewVendorManager("/test", WithVendorFetcherFactory(ff))

	assert.NotNil(t, manager)
	assert.NotNil(t, manager.fetcherFactory)
}
