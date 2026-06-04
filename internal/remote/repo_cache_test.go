package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

	// Create ctxloom/bundles/test.yaml
	bundleDir := filepath.Join(repoDir, "ctxloom", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))

	content := []byte("version: v1\ndescription: test bundle\n")
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), content, 0644))

	_, err = wt.Add("ctxloom/bundles/test.yaml")
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

func TestRepoCache_repoDirForURL_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	cache := NewRepoCache(base, AuthConfig{})

	// A crafted URL with ".." segments must not escape the cache dir, since
	// the result is later RemoveAll'd and cloned into.
	traversal := []string{
		"https://evil.com/../../../../etc/passwd",
		"https://host/a/../../../../../tmp/pwned",
		"ssh://git@host/../../outside",
	}
	for _, u := range traversal {
		dir := cache.repoDirForURL(u)
		rel, err := filepath.Rel(base, dir)
		require.NoError(t, err, "repoDirForURL(%q) = %q", u, dir)
		assert.False(t, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
			"repoDirForURL(%q) = %q escaped base %q (rel=%q)", u, dir, base, rel)
	}
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

// TestRepoCache_EnsureRef_ResolvesOlderRef verifies that a full clone makes an
// older ref (a tag behind HEAD) locally resolvable without any extra fetch —
// the clone carries complete history and all tags.
func TestRepoCache_EnsureRef_ResolvesOlderRef(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")

	repo, err := git.PlainInit(sourceDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("a\n"), 0644))
	_, err = wt.Add("a.txt")
	require.NoError(t, err)
	first, err := wt.Commit("first", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	_, err = repo.CreateTag("v0.1.0", first, nil)
	require.NoError(t, err)

	// Second commit so HEAD moves past the tag.
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "b.txt"), []byte("b\n"), 0644))
	_, err = wt.Add("b.txt")
	require.NoError(t, err)
	_, err = wt.Commit("second", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// EnsureRef with empty ref clones the full history.
	repoDir, err := cache.EnsureRef(context.Background(), "file://"+sourceDir, ForgeGitHub, "")
	require.NoError(t, err)
	assert.DirExists(t, repoDir)

	// The tag points at the older commit; the full clone already carries it, so
	// it is locally resolvable.
	repoDir2, err := cache.EnsureRef(context.Background(), "file://"+sourceDir, ForgeGitHub, "v0.1.0")
	require.NoError(t, err)
	assert.Equal(t, repoDir, repoDir2)

	clone, err := git.PlainOpen(repoDir2)
	require.NoError(t, err)
	_, err = clone.ResolveRevision(plumbing.Revision("refs/tags/v0.1.0"))
	require.NoError(t, err, "tag v0.1.0 must be locally resolvable in the full clone")
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

func TestRepoCache_authArgs(t *testing.T) {
	t.Run("github with token injects extraheader", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitHub: "test-token"})
		args := cache.authArgs("https://github.com/owner/repo", ForgeGitHub)
		require.Len(t, args, 2)
		assert.Equal(t, "-c", args[0])
		assert.Contains(t, args[1], "http.https://github.com/.extraheader=AUTHORIZATION: basic ")
		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:test-token"))
		assert.Contains(t, args[1], want)
	})

	t.Run("github without token injects nothing", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{})
		assert.Nil(t, cache.authArgs("https://github.com/owner/repo", ForgeGitHub))
	})

	t.Run("generic git uses ambient auth, no injection", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitHub: "test-token"})
		assert.Nil(t, cache.authArgs("https://gitlab.com/owner/repo", ForgeGitGeneric))
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
