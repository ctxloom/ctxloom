package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRepoCache_EnsureFullRepo exercises the eager full-clone entry point end
// to end through the system git binary against a local file:// origin: it must
// materialize a real working tree (.git present) reachable for later reads.
func TestRepoCache_EnsureFullRepo(t *testing.T) {
	sourceRepo := createTestRepo(t, t.TempDir())
	cache := NewRepoCache(t.TempDir(), AuthConfig{})

	repoDir, err := cache.EnsureFullRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.True(t, isGitRepo(repoDir), "EnsureFullRepo must leave a usable clone with a .git dir")

	// The cloned content is readable: the fixture's bundle file is present.
	_, statErr := os.Stat(filepath.Join(repoDir, ".ctxloom", "content", "bundles", "test.yaml"))
	assert.NoError(t, statErr, "full clone must carry the repo's working tree")
}

// TestRepoCache_cloneToken_UsesForgeResolver pins the WithForgeResolver path:
// the clone token for a github forge is read from the resolved forge's
// token_env, so authEnv injects that token rather than the ambient one.
func TestRepoCache_cloneToken_UsesForgeResolver(t *testing.T) {
	testsupport.Isolate(t) // clear ambient GITHUB_TOKEN/GH_TOKEN
	t.Setenv("GHE_TOKEN", "from-token-env")

	resolver := func(string) ResolvedForge {
		return ResolvedForge{Type: ForgeGitHub, TokenEnv: "GHE_TOKEN"}
	}
	cache := NewRepoCache("", AuthConfig{GitHub: "ambient-token"}, WithForgeResolver(resolver))

	env := cache.authEnv("https://github.com/owner/repo", ForgeGitHub)
	require.Len(t, env, 3)
	wantToken := base64.StdEncoding.EncodeToString([]byte("x-access-token:from-token-env"))
	assert.Contains(t, env[2], wantToken, "resolver's token_env value must win over the ambient token")

	ambient := base64.StdEncoding.EncodeToString([]byte("x-access-token:ambient-token"))
	assert.NotContains(t, env[2], ambient)
}

// TestRepoCache_cloneToken_ResolverFallsBackToAmbient covers the branch where
// the resolved forge names a token_env that is unset: Token falls back to the
// ambient auth token.
func TestRepoCache_cloneToken_ResolverFallsBackToAmbient(t *testing.T) {
	testsupport.Isolate(t)
	resolver := func(string) ResolvedForge {
		return ResolvedForge{Type: ForgeGitHub, TokenEnv: "UNSET_TOKEN"}
	}
	cache := NewRepoCache("", AuthConfig{GitHub: "ambient-token"}, WithForgeResolver(resolver))

	env := cache.authEnv("https://github.com/owner/repo", ForgeGitHub)
	require.Len(t, env, 3)
	want := base64.StdEncoding.EncodeToString([]byte("x-access-token:ambient-token"))
	assert.Contains(t, env[2], want)
}

// TestRepoCache_RepoDirForURL covers the cache-path derivation operations uses.
func TestRepoCache_RepoDirForURL(t *testing.T) {
	cache := NewRepoCache("/tmp/cache", AuthConfig{})
	got, err := cache.RepoDirForURL("https://github.com/owner/repo")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/cache/github.com/owner/repo", got)
}
