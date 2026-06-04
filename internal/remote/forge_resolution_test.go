package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveForgeForURLWith covers the public per-URL forge resolver across
// its full precedence chain: explicit label, host match against a configured
// base_url, built-in github (for github.com and owner/repo shorthand), and the
// generic git fallback — plus the token_env default and base_url backfill that
// resolvedFromConfig applies.
func TestResolveForgeForURLWith(t *testing.T) {
	ghe := ForgeConfig{Type: string(ForgeGitHub), Body: map[string]any{
		"base_url":  "https://ghe.corp",
		"api_url":   "https://ghe.corp/api/v3",
		"token_env": "GHE_TOKEN",
	}}
	customGit := ForgeConfig{Type: string(ForgeGitGeneric)} // no base_url

	tests := []struct {
		name   string
		url    string
		label  string
		forges map[string]ForgeConfig
		want   ResolvedForge
	}{
		{
			name:   "github_url_builtin_defaults",
			url:    "https://github.com/owner/repo",
			forges: MergeForges(nil),
			want:   ResolvedForge{Type: ForgeGitHub, BaseURL: "https://github.com", TokenEnv: DefaultGitHubTokenEnv},
		},
		{
			name:   "shorthand_owner_repo_is_github",
			url:    "owner/repo",
			forges: MergeForges(nil),
			want:   ResolvedForge{Type: ForgeGitHub, BaseURL: "https://github.com", TokenEnv: DefaultGitHubTokenEnv},
		},
		{
			name:   "other_host_is_generic_git",
			url:    "https://gitlab.com/owner/repo",
			forges: MergeForges(nil),
			want:   ResolvedForge{Type: ForgeGitGeneric, BaseURL: "https://gitlab.com", TokenEnv: ""},
		},
		{
			name:   "explicit_label_wins",
			url:    "https://github.com/owner/repo", // host says github.com, but label points at GHE
			label:  "ghe",
			forges: MergeForges(map[string]ForgeConfig{"ghe": ghe}),
			want:   ResolvedForge{Type: ForgeGitHub, BaseURL: "https://ghe.corp", APIURL: "https://ghe.corp/api/v3", TokenEnv: "GHE_TOKEN"},
		},
		{
			name:   "unknown_label_falls_through_to_host_detect",
			url:    "https://github.com/owner/repo",
			label:  "does-not-exist",
			forges: MergeForges(map[string]ForgeConfig{"ghe": ghe}),
			want:   ResolvedForge{Type: ForgeGitHub, BaseURL: "https://github.com", TokenEnv: DefaultGitHubTokenEnv},
		},
		{
			name:   "host_match_without_label",
			url:    "https://ghe.corp/owner/repo",
			forges: MergeForges(map[string]ForgeConfig{"ghe": ghe}),
			want:   ResolvedForge{Type: ForgeGitHub, BaseURL: "https://ghe.corp", APIURL: "https://ghe.corp/api/v3", TokenEnv: "GHE_TOKEN"},
		},
		{
			name:   "base_url_backfills_from_remote_host",
			url:    "https://git.internal/owner/repo",
			label:  "custom",
			forges: MergeForges(map[string]ForgeConfig{"custom": customGit}),
			want:   ResolvedForge{Type: ForgeGitGeneric, BaseURL: "https://git.internal", TokenEnv: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveForgeForURLWith(tt.url, tt.label, tt.forges)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMergeForges confirms the built-ins are always present and that a user
// entry overrides the same key while leaving the other built-in intact.
func TestMergeForges(t *testing.T) {
	t.Run("nil_user_yields_builtins", func(t *testing.T) {
		m := MergeForges(nil)
		assert.Contains(t, m, string(ForgeGitHub))
		assert.Contains(t, m, string(ForgeGitGeneric))
	})

	t.Run("user_overrides_builtin_and_adds", func(t *testing.T) {
		override := ForgeConfig{Type: string(ForgeGitHub), Body: map[string]any{"token_env": "CUSTOM"}}
		extra := ForgeConfig{Type: string(ForgeGitGeneric)}
		m := MergeForges(map[string]ForgeConfig{string(ForgeGitHub): override, "internal": extra})

		assert.Equal(t, "CUSTOM", m[string(ForgeGitHub)].TokenEnv(), "user github entry must replace the built-in")
		assert.Contains(t, m, "internal", "user-added forge must be present")
		assert.Contains(t, m, string(ForgeGitGeneric), "untouched built-in must survive")
	})
}

// TestNewForgeFetcher covers the fetcher-factory routing: github yields an
// authenticated GitHubFetcher whose token comes from the resolved forge, while
// the generic git adapter and unknown types are rejected (reads go through the
// clone cache instead).
func TestNewForgeFetcher(t *testing.T) {
	t.Run("github_with_token_env", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("GHE_TOKEN", "ghe-secret")
		rf := ResolvedForge{Type: ForgeGitHub, APIURL: "https://ghe.corp/api/v3", TokenEnv: "GHE_TOKEN"}

		f, err := NewForgeFetcher("https://ghe.corp/o/r", rf, AuthConfig{})
		require.NoError(t, err)
		gh, ok := f.(*GitHubFetcher)
		require.True(t, ok)
		assert.True(t, gh.hasToken)
		assert.Equal(t, "ghe-secret", gh.token)
	})

	t.Run("github_apiurl_routes_to_enterprise_endpoint", func(t *testing.T) {
		testsupport.Isolate(t)
		rf := ResolvedForge{Type: ForgeGitHub, APIURL: "https://ghe.corp/api/v3"}

		f, err := NewForgeFetcher("https://ghe.corp/o/r", rf, AuthConfig{})
		require.NoError(t, err)
		gh := f.(*GitHubFetcher)
		rc, ok := gh.client.(*realGitHubClient)
		require.True(t, ok)
		assert.Contains(t, rc.client.BaseURL.String(), "ghe.corp",
			"a resolved APIURL must route the client at the enterprise endpoint, not api.github.com")
	})

	t.Run("github_without_token_is_unauthenticated", func(t *testing.T) {
		testsupport.Isolate(t) // clears ambient GITHUB_TOKEN so the fetcher stays tokenless
		rf := ResolvedForge{Type: ForgeGitHub, TokenEnv: DefaultGitHubTokenEnv}

		f, err := NewForgeFetcher("https://github.com/o/r", rf, AuthConfig{})
		require.NoError(t, err)
		gh, ok := f.(*GitHubFetcher)
		require.True(t, ok)
		assert.False(t, gh.hasToken)
		assert.Empty(t, gh.token)
	})

	t.Run("generic_git_has_no_api_fetcher", func(t *testing.T) {
		_, err := NewForgeFetcher("https://gitlab.com/o/r", ResolvedForge{Type: ForgeGitGeneric}, AuthConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generic git forge has no API fetcher")
	})

	t.Run("unknown_type_is_rejected", func(t *testing.T) {
		_, err := NewForgeFetcher("https://x/o/r", ResolvedForge{Type: ForgeType("svn")}, AuthConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported forge type")
	})
}
