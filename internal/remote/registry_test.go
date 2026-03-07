package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	t.Run("creates registry with default path", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldDir, _ := os.Getwd()
		_ = os.Chdir(tmpDir)
		defer func() { _ = os.Chdir(oldDir) }()

		registry, err := NewRegistry("")
		require.NoError(t, err)
		assert.NotNil(t, registry)
		assert.Equal(t, filepath.Join(".scm", "remotes.yaml"), registry.configPath)
	})

	t.Run("creates registry with custom path", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "custom.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)
		assert.NotNil(t, registry)
		assert.Equal(t, configPath, registry.configPath)
	})

	t.Run("loads existing config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		configContent := `
remotes:
  github:
    url: https://github.com/test/repo
    version: v1
`
		require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)
		assert.True(t, registry.Has("github"))
	})

	t.Run("handles invalid YAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		require.NoError(t, os.WriteFile(configPath, []byte("invalid: yaml: [["), 0644))

		_, err := NewRegistry(configPath)
		require.Error(t, err)
	})
}

func TestRegistry_Add(t *testing.T) {
	t.Run("adds new remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo")
		require.NoError(t, err)
		assert.True(t, registry.Has("test"))

		remote, err := registry.Get("test")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/owner/repo", remote.URL)
		assert.Equal(t, "v1", remote.Version)
	})

	t.Run("normalizes URL", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "owner/repo")
		require.NoError(t, err)

		remote, err := registry.Get("test")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/owner/repo", remote.URL)
	})

	t.Run("rejects duplicate name", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo1")
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("rejects duplicate URL", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("remote1", "https://github.com/owner/repo")
		require.NoError(t, err)

		err = registry.Add("remote2", "https://github.com/owner/repo")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already points to this URL")
	})
}

func TestRegistry_AddWithVersion(t *testing.T) {
	t.Run("adds remote with specified version", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.AddWithVersion("test", "https://github.com/owner/repo", "v2")
		require.NoError(t, err)

		remote, err := registry.Get("test")
		require.NoError(t, err)
		assert.Equal(t, "v2", remote.Version)
	})

	t.Run("rejects duplicate name", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.AddWithVersion("test", "https://github.com/owner/repo1", "v1")
		require.NoError(t, err)

		err = registry.AddWithVersion("test", "https://github.com/owner/repo2", "v2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("rejects duplicate URL", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.AddWithVersion("remote1", "https://github.com/owner/repo", "v1")
		require.NoError(t, err)

		err = registry.AddWithVersion("remote2", "https://github.com/owner/repo", "v2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already points to this URL")
	})

	t.Run("normalizes URL", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.AddWithVersion("test", "owner/repo", "v2")
		require.NoError(t, err)

		remote, err := registry.Get("test")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/owner/repo", remote.URL)
	})
}

func TestRegistry_Remove(t *testing.T) {
	t.Run("removes existing remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo")
		require.NoError(t, err)
		assert.True(t, registry.Has("test"))

		err = registry.Remove("test")
		require.NoError(t, err)
		assert.False(t, registry.Has("test"))
	})

	t.Run("error on non-existent remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Remove("nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestRegistry_Get(t *testing.T) {
	t.Run("gets existing remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo")
		require.NoError(t, err)

		remote, err := registry.Get("test")
		require.NoError(t, err)
		assert.Equal(t, "test", remote.Name)
		assert.Equal(t, "https://github.com/owner/repo", remote.URL)
	})

	t.Run("error on non-existent remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		_, err = registry.Get("nonexistent")
		require.Error(t, err)
	})

	t.Run("returns copy not reference", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		err = registry.Add("test", "https://github.com/owner/repo")
		require.NoError(t, err)

		remote1, _ := registry.Get("test")
		remote2, _ := registry.Get("test")

		remote1.URL = "modified"
		assert.Equal(t, "https://github.com/owner/repo", remote2.URL)
	})
}

func TestRegistry_List(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "remotes.yaml")

	registry, err := NewRegistry(configPath)
	require.NoError(t, err)

	t.Run("empty list", func(t *testing.T) {
		list := registry.List()
		assert.Empty(t, list)
	})

	_ = registry.Add("zebra", "https://github.com/z/z")
	_ = registry.Add("alpha", "https://github.com/a/a")
	_ = registry.Add("mike", "https://github.com/m/m")

	t.Run("returns sorted list", func(t *testing.T) {
		list := registry.List()
		assert.Len(t, list, 3)
		assert.Equal(t, "alpha", list[0].Name)
		assert.Equal(t, "mike", list[1].Name)
		assert.Equal(t, "zebra", list[2].Name)
	})
}

func TestRegistry_Has(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "remotes.yaml")

	registry, err := NewRegistry(configPath)
	require.NoError(t, err)

	assert.False(t, registry.Has("test"))

	_ = registry.Add("test", "https://github.com/owner/repo")
	assert.True(t, registry.Has("test"))
}

func TestRegistry_FindByURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "remotes.yaml")

	registry, err := NewRegistry(configPath)
	require.NoError(t, err)

	_ = registry.Add("github-test", "https://github.com/owner/repo")

	t.Run("finds existing URL", func(t *testing.T) {
		name, found := registry.FindByURL("https://github.com/owner/repo")
		assert.True(t, found)
		assert.Equal(t, "github-test", name)
	})

	t.Run("normalizes URL for search", func(t *testing.T) {
		name, found := registry.FindByURL("owner/repo")
		assert.True(t, found)
		assert.Equal(t, "github-test", name)
	})

	t.Run("returns false for unknown URL", func(t *testing.T) {
		_, found := registry.FindByURL("https://github.com/other/repo")
		assert.False(t, found)
	})
}

func TestRegistry_SetVersion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "remotes.yaml")

	registry, err := NewRegistry(configPath)
	require.NoError(t, err)

	t.Run("updates existing remote version", func(t *testing.T) {
		_ = registry.Add("test", "https://github.com/owner/repo")

		err = registry.SetVersion("test", "v2")
		require.NoError(t, err)

		remote, _ := registry.Get("test")
		assert.Equal(t, "v2", remote.Version)
	})

	t.Run("error on non-existent remote", func(t *testing.T) {
		err = registry.SetVersion("nonexistent", "v2")
		require.Error(t, err)
	})
}

func TestRegistry_GetOrCreateByURL(t *testing.T) {
	t.Run("returns existing remote if URL matches", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		_ = registry.Add("existing", "https://github.com/owner/repo")

		remote, err := registry.GetOrCreateByURL("https://github.com/owner/repo", "v1")
		require.NoError(t, err)
		assert.Equal(t, "existing", remote.Name)
	})

	t.Run("creates new remote with repo name", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		remote, err := registry.GetOrCreateByURL("https://github.com/owner/myrepo", "v1")
		require.NoError(t, err)
		assert.Equal(t, "myrepo", remote.Name)
		assert.True(t, registry.Has("myrepo"))
	})

	t.Run("handles name conflict with suffix", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		_ = registry.Add("repo", "https://github.com/first/repo")

		remote, err := registry.GetOrCreateByURL("https://github.com/second/repo", "v1")
		require.NoError(t, err)
		assert.Equal(t, "repo-2", remote.Name)
	})
}

func TestRegistry_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "remotes.yaml")

	// Create and populate registry
	registry1, err := NewRegistry(configPath)
	require.NoError(t, err)
	_ = registry1.Add("test1", "https://github.com/owner/repo1")
	_ = registry1.Add("test2", "https://github.com/owner/repo2")

	// Create new registry from same file
	registry2, err := NewRegistry(configPath)
	require.NoError(t, err)

	assert.True(t, registry2.Has("test1"))
	assert.True(t, registry2.Has("test2"))

	// Verify URLs loaded correctly
	remote, _ := registry2.Get("test1")
	assert.Equal(t, "https://github.com/owner/repo1", remote.URL)
}

func TestRegistry_GetFetcher(t *testing.T) {
	t.Run("returns error for non-existent remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "remotes.yaml")

		registry, err := NewRegistry(configPath)
		require.NoError(t, err)

		_, err = registry.GetFetcher("nonexistent", AuthConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}
