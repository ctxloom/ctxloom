package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestRepo creates a bare git repo with some files for testing.
func createTestRepo(t *testing.T, dir string) string {
	t.Helper()

	repoDir := filepath.Join(dir, "test-repo")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create ctxloom/v1/bundles/test.yaml
	bundleDir := filepath.Join(repoDir, "ctxloom", "v1", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))

	content := []byte("version: v1\ndescription: test bundle\n")
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), content, 0644))

	_, err = wt.Add("ctxloom/v1/bundles/test.yaml")
	require.NoError(t, err)

	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return repoDir
}

func TestRepoCache_EnsureRepo_Clone(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	repoDir, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

func TestRepoCache_EnsureRepo_AlreadyCloned(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// First clone
	repoDir1, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)

	// Second call should return immediately (already cloned, no fetch)
	repoDir2, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.Equal(t, repoDir1, repoDir2)
}

func TestRepoCache_EnsureRepo_CorruptClone(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Create a corrupt directory where the clone would go
	cloneURL := "file://" + sourceRepo
	expectedDir := cache.repoDirForURL(cloneURL)
	require.NoError(t, os.MkdirAll(expectedDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(expectedDir, "garbage"), []byte("not a repo"), 0644))

	// Should delete and re-clone
	repoDir, err := cache.EnsureRepo(context.Background(), cloneURL, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

func TestRepoCache_UpdateRepo(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// First ensure (clone)
	repoDir, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)

	// Update should fetch
	repoDir2, err := cache.UpdateRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.Equal(t, repoDir, repoDir2)
}

func TestRepoCache_UpdateRepo_NotYetCloned(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Update on a repo that isn't cloned yet should clone it
	repoDir, err := cache.UpdateRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

func TestRepoCache_repoDirForURL(t *testing.T) {
	cache := NewRepoCache("/tmp/cache", AuthConfig{})

	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "https github",
			url:  "https://github.com/owner/repo",
			want: "/tmp/cache/github.com/owner/repo",
		},
		{
			name: "https gitlab",
			url:  "https://gitlab.com/owner/repo",
			want: "/tmp/cache/gitlab.com/owner/repo",
		},
		{
			name: "with .git suffix",
			url:  "https://github.com/owner/repo.git",
			want: "/tmp/cache/github.com/owner/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cache.repoDirForURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepoCache_authMethod(t *testing.T) {
	t.Run("github with token", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitHub: "test-token"})
		auth := cache.authMethod(ForgeGitHub)
		assert.NotNil(t, auth)
	})

	t.Run("github without token", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{})
		auth := cache.authMethod(ForgeGitHub)
		assert.Nil(t, auth)
	})

	t.Run("gitlab with token", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitLab: "test-token"})
		auth := cache.authMethod(ForgeGitLab)
		assert.NotNil(t, auth)
	})

	t.Run("gitlab without token", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{})
		auth := cache.authMethod(ForgeGitLab)
		assert.Nil(t, auth)
	})
}

func TestNormalizeCloneURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo"},
		{"owner/repo", "https://github.com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeCloneURL(tt.input))
		})
	}
}
