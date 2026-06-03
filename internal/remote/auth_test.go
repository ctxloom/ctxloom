package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadAuth_GitHubToken(t *testing.T) {
	t.Run("GITHUB_TOKEN", func(t *testing.T) {
		// Clear all token env vars to isolate the test
		t.Setenv("GITHUB_TOKEN", "gh-token-123")
		t.Setenv("GH_TOKEN", "")

		auth := LoadAuth("")
		assert.Equal(t, "gh-token-123", auth.GitHub)
	})

	t.Run("GH_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gh-short-token")

		auth := LoadAuth("")
		assert.Equal(t, "gh-short-token", auth.GitHub)
	})

	t.Run("GH_TOKEN overrides GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "first-token")
		t.Setenv("GH_TOKEN", "second-token")

		auth := LoadAuth("")
		// GH_TOKEN is checked after GITHUB_TOKEN, so it wins
		assert.Equal(t, "second-token", auth.GitHub)
	})
}

func TestLoadAuth_NoTokens(t *testing.T) {
	// Clear all token env vars to isolate the test
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	auth := LoadAuth("")
	assert.Empty(t, auth.GitHub)
}

func TestAuthConfig_HasGitHubAuth(t *testing.T) {
	tests := []struct {
		name   string
		auth   AuthConfig
		expect bool
	}{
		{"with token", AuthConfig{GitHub: "token"}, true},
		{"empty token", AuthConfig{GitHub: ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.auth.HasGitHubAuth())
		})
	}
}

func TestAuthConfig_TokenForForge(t *testing.T) {
	auth := AuthConfig{GitHub: "gh-token"}

	tests := []struct {
		name   string
		forge  ForgeType
		expect string
	}{
		{"GitHub", ForgeGitHub, "gh-token"},
		{"generic git", ForgeGitGeneric, ""},
		{"unknown forge", ForgeType("unknown"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, auth.TokenForForge(tt.forge))
		})
	}
}
