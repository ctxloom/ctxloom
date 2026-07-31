package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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

// TestRepoCache_cloneToken_AmbientTokenIsScopedToCloneHost pins the boundary an
// UNNAMED github credential must not cross: the ambient token (GITHUB_TOKEN /
// GH_TOKEN, and the per-type token_env default that is the same variable) is a
// github.com credential, so it may only be sent to github.com.
//
// The two tests above look like they defend the ambient fallback, but both
// clone from github.com — the favourable input. Neither distinguishes
// github.com from an enterprise host, so neither would have caught the
// github.com PAT being handed to github.corp.example on every clone and fetch.
// This is the pin they never provided.
func TestRepoCache_cloneToken_AmbientTokenIsScopedToCloneHost(t *testing.T) {
	const (
		enterpriseURL = "https://github.corp.example/owner/repo.git"
		dotComURL     = "https://github.com/owner/repo.git"
		ambientPAT    = "pat-for-github-dot-com"
	)
	corpForges := func(body map[string]any) map[string]ForgeConfig {
		return MergeForges(map[string]ForgeConfig{"corp": {Type: string(ForgeGitHub), Body: body}})
	}
	cacheFor := func(forges map[string]ForgeConfig) *RepoCache {
		return NewRepoCache("", AuthConfig{GitHub: ambientPAT},
			WithForgeResolver(func(u string) ResolvedForge { return ResolveForgeForURLWith(u, "", forges) }))
	}

	t.Run("no token_env: the ambient token does not reach an enterprise host", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv(DefaultGitHubTokenEnv, ambientPAT)
		cache := cacheFor(corpForges(map[string]any{"base_url": "https://github.corp.example"}))

		assert.False(t, emitsAuthHeader(cache.authEnv(enterpriseURL, ForgeGitHub)),
			"an ordinary `forges: {corp: {type: github, base_url: https://github.corp.example}}` "+
				"must not put the github.com credential on the wire to github.corp.example")
	})

	t.Run("no token_env: the ambient token still reaches github.com", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv(DefaultGitHubTokenEnv, ambientPAT)
		cache := cacheFor(corpForges(map[string]any{"base_url": "https://github.corp.example"}))

		env := envMap(t, cache.authEnv(dotComURL, ForgeGitHub))
		assert.Equal(t, "http.https://github.com/.extraheader", env["GIT_CONFIG_KEY_0"])
		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + ambientPAT))
		assert.Equal(t, "AUTHORIZATION: basic "+want, env["GIT_CONFIG_VALUE_0"],
			"scoping the ambient credential must not stop it reaching the host it belongs to")
	})

	t.Run("an explicit token_env still reaches an enterprise host", func(t *testing.T) {
		// The thing this scoping must not break: a per-forge credential the user
		// NAMED is not the github.com credential, and travels wherever its forge
		// points. Protected by a test rather than by hope.
		testsupport.Isolate(t)
		t.Setenv(DefaultGitHubTokenEnv, ambientPAT)
		t.Setenv("CORP_TOKEN", "corp-scoped-token")
		cache := cacheFor(corpForges(map[string]any{
			"base_url":  "https://github.corp.example",
			"token_env": "CORP_TOKEN",
		}))

		env := envMap(t, cache.authEnv(enterpriseURL, ForgeGitHub))
		assert.Equal(t, "http.https://github.corp.example/.extraheader", env["GIT_CONFIG_KEY_0"])
		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:corp-scoped-token"))
		assert.Equal(t, "AUTHORIZATION: basic "+want, env["GIT_CONFIG_VALUE_0"])
	})

	t.Run("with no resolver at all the ambient token is scoped the same way", func(t *testing.T) {
		// NewRepoCache without WithForgeResolver is a real construction (see the
		// registry-read failure path in operations.NewRepoCache), and it reaches
		// AuthConfig.GitHub directly.
		testsupport.Isolate(t)
		cache := NewRepoCache("", AuthConfig{GitHub: ambientPAT})

		assert.False(t, emitsAuthHeader(cache.authEnv(enterpriseURL, ForgeGitHub)),
			"the resolver-less path carries the same github.com credential and needs the same scope")
		assert.True(t, emitsAuthHeader(cache.authEnv(dotComURL, ForgeGitHub)))
	})
}

// emitsAuthHeader reports whether authEnv output carries an Authorization
// header. It returns a bool rather than handing the entries to an assertion
// because a failed assertion PRINTS its argument, and an environment dump in a
// test log is how credentials escape (same reason envMap exists).
func emitsAuthHeader(entries []string) bool {
	for _, e := range entries {
		if strings.HasPrefix(e, "GIT_CONFIG_VALUE_") && strings.Contains(e, "AUTHORIZATION:") {
			return true
		}
	}
	return false
}
