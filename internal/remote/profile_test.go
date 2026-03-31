package remote

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProfileRefs(t *testing.T) {
	tests := []struct {
		name     string
		refs     []string
		itemType ItemType
		want     int // Number of remote refs
	}{
		{
			name:     "all local",
			refs:     []string{"security", "golang"},
			itemType: ItemTypeBundle,
			want:     0,
		},
		{
			name:     "all remote",
			refs:     []string{"alice/security", "bob/golang"},
			itemType: ItemTypeBundle,
			want:     2,
		},
		{
			name:     "mixed",
			refs:     []string{"local", "alice/remote", "another-local"},
			itemType: ItemTypeProfile,
			want:     1,
		},
		{
			name:     "with version",
			refs:     []string{"alice/security@v1.0.0", "bob/golang@main"},
			itemType: ItemTypeBundle,
			want:     2,
		},
		{
			name:     "nested path",
			refs:     []string{"alice/lang/go/testing"},
			itemType: ItemTypeBundle,
			want:     1,
		},
		{
			name:     "empty",
			refs:     []string{},
			itemType: ItemTypeBundle,
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseProfileRefs(tt.refs, tt.itemType)
			if len(got) != tt.want {
				t.Errorf("ParseProfileRefs() returned %d refs, want %d", len(got), tt.want)
			}

			// Verify item type is set correctly
			for _, ref := range got {
				if ref.ItemType != tt.itemType {
					t.Errorf("ItemType = %v, want %v", ref.ItemType, tt.itemType)
				}
			}
		})
	}
}

func TestParseProfileRefs_RefFormat(t *testing.T) {
	refs := []string{"alice/go-tools@v1.0.0"}
	results := ParseProfileRefs(refs, ItemTypeBundle)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Ref != "alice/go-tools@v1.0.0" {
		t.Errorf("Ref = %q, want %q", results[0].Ref, "alice/go-tools@v1.0.0")
	}

	if results[0].Cached {
		t.Error("new ref should not be cached")
	}
}

func TestRemoteRef_Fields(t *testing.T) {
	ref := RemoteRef{
		Ref:      "alice/go-tools",
		ItemType: ItemTypeBundle,
		Cached:   false,
	}

	if ref.Ref != "alice/go-tools" {
		t.Errorf("Ref = %q, want %q", ref.Ref, "alice/go-tools")
	}
	if ref.ItemType != ItemTypeBundle {
		t.Errorf("ItemType = %v, want %v", ref.ItemType, ItemTypeBundle)
	}
	if ref.Cached {
		t.Error("Cached should be false")
	}
}

func TestNewProfileDeps(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	t.Run("creates with defaults", func(t *testing.T) {
		deps := NewProfileDeps(registry, AuthConfig{})
		assert.NotNil(t, deps)
		assert.NotNil(t, deps.puller)
		assert.NotNil(t, deps.fs)
		assert.Equal(t, registry, deps.registry)
	})

	t.Run("accepts custom filesystem", func(t *testing.T) {
		customFS := afero.NewMemMapFs()
		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsFS(customFS))
		assert.Equal(t, customFS, deps.fs)
	})

	t.Run("accepts custom puller", func(t *testing.T) {
		customPuller := NewPuller(registry, AuthConfig{})
		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsPuller(customPuller))
		assert.Equal(t, customPuller, deps.puller)
	})
}

func TestProfileDeps_CheckCached(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	t.Run("marks cached refs", func(t *testing.T) {
		// Create a cached bundle file
		require.NoError(t, fs.MkdirAll("/test/bundles/alice", 0755))
		require.NoError(t, afero.WriteFile(fs, "/test/bundles/alice/security.yaml", []byte("test\n"), 0644))

		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsFS(fs))

		refs := []RemoteRef{
			{Ref: "alice/security", ItemType: ItemTypeBundle, Cached: false},
			{Ref: "bob/golang", ItemType: ItemTypeBundle, Cached: false},
		}

		result := deps.CheckCached(refs, "/test")

		// alice/security should be marked as cached
		assert.True(t, result[0].Cached)
		// bob/golang should not be cached
		assert.False(t, result[1].Cached)
	})

	t.Run("uses default base dir", func(t *testing.T) {
		require.NoError(t, fs.MkdirAll(".ctxloom/bundles/alice", 0755))
		require.NoError(t, afero.WriteFile(fs, ".ctxloom/bundles/alice/security.yaml", []byte("test\n"), 0644))

		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsFS(fs))

		refs := []RemoteRef{
			{Ref: "alice/security", ItemType: ItemTypeBundle, Cached: false},
		}

		result := deps.CheckCached(refs, "")
		assert.True(t, result[0].Cached)
	})

	t.Run("handles invalid refs gracefully", func(t *testing.T) {
		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsFS(fs))

		refs := []RemoteRef{
			{Ref: "invalid", ItemType: ItemTypeBundle, Cached: false}, // Not a valid remote ref
		}

		result := deps.CheckCached(refs, "/test")
		assert.False(t, result[0].Cached)
	})
}

func TestProfileDeps_PullDeps(t *testing.T) {
	t.Run("returns early for no uncached deps", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		deps := NewProfileDeps(registry, AuthConfig{}, WithProfileDepsFS(fs))

		refs := []RemoteRef{
			{Ref: "alice/security", ItemType: ItemTypeBundle, Cached: true},
		}

		var buf bytes.Buffer
		result, err := deps.PullDeps(context.Background(), refs, PullOptions{
			Stdout: &buf,
			Stdin:  strings.NewReader(""),
		})

		require.NoError(t, err)
		assert.Empty(t, result.Pulled)
		assert.Empty(t, result.Failed)
		assert.Empty(t, result.Skipped)
	})

	t.Run("pulls uncached deps", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Create registry with remote
		require.NoError(t, fs.MkdirAll("/test", 0755))
		registry, _ := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

		// Create mock fetcher
		mf := newMockFetcher()
		mf.files["scm/v1/bundles/security.yaml"] = []byte("description: Security\n")
		mf.refs["main"] = "abc123"

		// Create puller with mocked dependencies
		tc := &mockTerminalChecker{isReader: true}
		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithTerminalChecker(tc),
			WithFetcherFactory(mockFetcherFactory(mf)),
		)

		deps := NewProfileDeps(registry, AuthConfig{},
			WithProfileDepsFS(fs),
			WithProfileDepsPuller(puller),
		)

		refs := []RemoteRef{
			{Ref: "alice/security", ItemType: ItemTypeBundle, Cached: false},
		}

		var buf bytes.Buffer
		result, err := deps.PullDeps(context.Background(), refs, PullOptions{
			Force:    true,
			LocalDir: "/test",
			Stdout:   &buf,
			Stdin:    strings.NewReader("y\n"),
		})

		require.NoError(t, err)
		assert.Len(t, result.Pulled, 1)
		assert.Contains(t, result.Pulled[0], "alice/security")
	})

	t.Run("tracks failed pulls", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		require.NoError(t, fs.MkdirAll("/test", 0755))
		registry, _ := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

		// Mock fetcher that returns file not found
		mf := newMockFetcher()
		// Note: not setting the file so fetch will fail

		tc := &mockTerminalChecker{isReader: true}
		puller := NewPuller(registry, AuthConfig{},
			WithPullerFS(fs),
			WithTerminalChecker(tc),
			WithFetcherFactory(mockFetcherFactory(mf)),
		)

		deps := NewProfileDeps(registry, AuthConfig{},
			WithProfileDepsFS(fs),
			WithProfileDepsPuller(puller),
		)

		refs := []RemoteRef{
			{Ref: "alice/security", ItemType: ItemTypeBundle, Cached: false},
		}

		var buf bytes.Buffer
		result, err := deps.PullDeps(context.Background(), refs, PullOptions{
			Force:    true,
			LocalDir: "/test",
			Stdout:   &buf,
			Stdin:    strings.NewReader(""),
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to pull")
		assert.Len(t, result.Failed, 1)
	})
}

func TestResolveProfileDeps(t *testing.T) {
	t.Run("returns nil for empty bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		err := ResolveProfileDeps(context.Background(), []string{}, registry, AuthConfig{}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("returns nil for only local bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		err := ResolveProfileDeps(context.Background(), []string{"local-bundle"}, registry, AuthConfig{}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("processes remote bundles with uncached deps", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Setup registry with remote
		require.NoError(t, fs.MkdirAll("/test", 0755))
		registry, _ := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

		// Create mock fetcher
		mf := newMockFetcher()
		mf.files["scm/v1/bundles/security.yaml"] = []byte("description: Security\n")
		mf.refs["main"] = "abc123"

		// Manually set up the ProfileDeps with mocked puller
		// Since ResolveProfileDeps creates its own ProfileDeps, we can't directly inject,
		// but we can at least test the error path with invalid registry
		var buf bytes.Buffer
		err := ResolveProfileDeps(context.Background(),
			[]string{"alice/security"},
			registry,
			AuthConfig{},
			&buf,
			strings.NewReader(""),
		)

		// Expected to fail because the fetcher isn't mocked and GitHub isn't accessible
		// This just tests that the function attempts to resolve deps
		assert.Error(t, err)
	})

	t.Run("handles nil stdout/stdin", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		// Should not panic even with nil I/O
		err := ResolveProfileDeps(context.Background(), []string{}, registry, AuthConfig{}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("filters out local refs from bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		// Mixed local and remote bundles - only remotes should be processed
		err := ResolveProfileDeps(context.Background(),
			[]string{"local-bundle", "alice/remote-bundle"},
			registry,
			AuthConfig{},
			bytes.NewBuffer([]byte{}),
			strings.NewReader(""),
		)

		// No remotes registered, so should error trying to fetch
		assert.Error(t, err)
	})
}
