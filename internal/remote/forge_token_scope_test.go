package remote

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// U093-F12 is CHARACTERIZED here, not fixed: the decision it turns on is a
// human's, and the escalation is recorded in the findings index.
//
// The claim is that a github.com personal access token is sent to a
// non-github.com host — a GitHub Enterprise forge whose token_env is unset
// receives GITHUB_TOKEN. It is accurate, and it is reachable in production:
// operations.NewRepoCache wires ResolveForgeForURLWith in as the cache's forge
// resolver, so RepoCache.cloneToken calls ResolvedForge.Token for every clone
// and fetch, and authEnv turns the result into an Authorization header scoped
// to the CLONE host.
//
// There are two independent routes to the same outcome, which is why a fix has
// to touch both. resolvedFromConfig defaults TokenEnv to GITHUB_TOKEN for
// every github-TYPED forge regardless of its host; and if that variable is
// unset, Token falls back to AuthConfig.GitHub, which LoadAuth fills from
// GITHUB_TOKEN or GH_TOKEN.
//
// These tests describe today's behaviour so it is visible rather than implied.
// They are expected to be INVERTED when the decision lands — do not read them
// as endorsing what they assert.
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

	t.Run("the token reaches the wire as an Authorization header for that host", func(t *testing.T) {
		// The consequence, not just the mechanism: what authEnv hands to git.
		testsupport.Isolate(t)
		cache := NewRepoCache("", AuthConfig{GitHub: "pat-for-github-dot-com"}, WithForgeResolver(resolveFor))

		env := envMap(t, cache.authEnv("https://github.corp.example/owner/repo", ForgeGitHub))
		assert.Equal(t, "http.https://github.corp.example/.extraheader", env["GIT_CONFIG_KEY_0"],
			"the header is scoped to the enterprise host")

		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:pat-for-github-dot-com"))
		assert.Equal(t, "AUTHORIZATION: basic "+want, env["GIT_CONFIG_VALUE_0"])
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
