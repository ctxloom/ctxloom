package remote

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The leak this file characterized is FIXED, and the fix is scoped at the
// clone URL — so most of what is described below still HAPPENS at the source
// and is pinned here on purpose, unchanged.
//
// The defect: a github.com personal access token was sent to a non-github.com
// host — a GitHub Enterprise forge whose token_env is unset received
// GITHUB_TOKEN. operations.NewRepoCache wires ResolveForgeForURLWith in as the
// clone cache's forge resolver, so RepoCache.cloneToken calls
// ResolvedForge.Token for every clone and fetch, and authEnv turned the result
// into an Authorization header scoped to the CLONE host.
//
// Two independent routes reach the same value: resolvedFromConfig defaults
// TokenEnv to GITHUB_TOKEN for every github-TYPED forge regardless of its
// host; and if that variable is unset, Token falls back to AuthConfig.GitHub,
// which LoadAuth fills from GITHUB_TOKEN or GH_TOKEN. Both still do that — the
// subtests below assert it — because the decision "may this credential go to
// THIS host" belongs where the destination is known, not where the value is
// read. That decision now lives in RepoCache.cloneToken, and the subtest at the
// bottom of this function is the one that changed: the token no longer reaches
// the wire for an enterprise host.
//
// So: subtests naming the SOURCE describe behaviour that is deliberately still
// there. Do not read them as a licence to hand rf.Token's result to a network
// destination without asking which host it is.
func TestResolvedForge_TokenScope_Characterization(t *testing.T) {
	enterprise := MergeForges(map[string]ForgeConfig{
		"corp": {Type: string(ForgeGitHub), Body: map[string]any{
			"base_url": "https://github.corp.example",
		}},
	})
	resolveFor := func(u string) ResolvedForge { return ResolveForgeForURLWith(u, "", enterprise) }

	t.Run("an enterprise forge with no token_env inherits the github.com default", func(t *testing.T) {
		rf := resolveFor("https://github.corp.example/owner/repo")
		require.Equal(t, ForgeGitHub, rf.Type)
		assert.Equal(t, "https://github.corp.example", rf.BaseURL)
		assert.Equal(t, DefaultGitHubTokenEnv, rf.TokenEnv,
			"route 1: the per-type default is applied without asking which host this forge serves")
	})

	t.Run("route 1: GITHUB_TOKEN is read for a host that is not github.com", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv(DefaultGitHubTokenEnv, "pat-for-github-dot-com")

		rf := resolveFor("https://github.corp.example/owner/repo")
		assert.Equal(t, "pat-for-github-dot-com", rf.Token(AuthConfig{}))
	})

	t.Run("route 2: the ambient fallback ignores the host too", func(t *testing.T) {
		testsupport.Isolate(t)
		rf := ResolvedForge{Type: ForgeGitHub, BaseURL: "https://github.corp.example", TokenEnv: "CORP_TOKEN_UNSET"}
		assert.Equal(t, "pat-for-github-dot-com", rf.Token(AuthConfig{GitHub: "pat-for-github-dot-com"}),
			"an explicitly-named token_env that is UNSET still falls through to the ambient token")
	})

	t.Run("but the token does not reach the wire for that host", func(t *testing.T) {
		// The consequence, not just the mechanism: what authEnv hands to git.
		// Both routes above still produce the github.com PAT; cloneToken is
		// where it stops. See TestRepoCache_cloneToken_AmbientTokenIsScopedToCloneHost
		// for the full boundary, including the positive cases.
		testsupport.Isolate(t)
		t.Setenv(DefaultGitHubTokenEnv, "pat-for-github-dot-com")
		cache := NewRepoCache("", AuthConfig{GitHub: "pat-for-github-dot-com"}, WithForgeResolver(resolveFor))

		assert.False(t, emitsAuthHeader(cache.authEnv("https://github.corp.example/owner/repo", ForgeGitHub)),
			"no Authorization header may be emitted for a host the ambient credential does not belong to")
	})

	t.Run("an explicit token_env is honoured, which is the documented opt-in", func(t *testing.T) {
		// The knob a fix would make mandatory already exists and already wins.
		testsupport.Isolate(t)
		t.Setenv("CORP_TOKEN", "corp-scoped-token")
		scoped := MergeForges(map[string]ForgeConfig{
			"corp": {Type: string(ForgeGitHub), Body: map[string]any{
				"base_url":  "https://github.corp.example",
				"token_env": "CORP_TOKEN",
			}},
		})
		rf := ResolveForgeForURLWith("https://github.corp.example/owner/repo", "", scoped)
		assert.Equal(t, "corp-scoped-token", rf.Token(AuthConfig{GitHub: "pat-for-github-dot-com"}))
	})
}

// envMap parses GIT_CONFIG_* entries into a map so assertions name the one key
// they care about. Never assert against the slice as a haystack: a failure
// prints it, and an environment dump in a test log is how credentials escape.
func envMap(t *testing.T, entries []string) map[string]string {
	t.Helper()
	require.Len(t, entries, 3, "authEnv emits exactly the three GIT_CONFIG_* entries")
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		require.True(t, ok)
		out[k] = v
	}
	return out
}
