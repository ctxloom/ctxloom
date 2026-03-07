package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadAuth_GitHubToken(t *testing.T) {
	t.Run("GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "gh-token-123")

		auth := LoadAuth("")
		assert.Equal(t, "gh-token-123", auth.GitHub)
		assert.Empty(t, auth.GitLab)
	})

	t.Run("GH_TOKEN", func(t *testing.T) {
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

func TestLoadAuth_GitLabToken(t *testing.T) {
	t.Run("GITLAB_TOKEN", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "gl-token-456")

		auth := LoadAuth("")
		assert.Equal(t, "gl-token-456", auth.GitLab)
		assert.Empty(t, auth.GitHub)
	})

	t.Run("GL_TOKEN", func(t *testing.T) {
		t.Setenv("GL_TOKEN", "gl-short-token")

		auth := LoadAuth("")
		assert.Equal(t, "gl-short-token", auth.GitLab)
	})

	t.Run("GL_TOKEN overrides GITLAB_TOKEN", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "first-gl-token")
		t.Setenv("GL_TOKEN", "second-gl-token")

		auth := LoadAuth("")
		assert.Equal(t, "second-gl-token", auth.GitLab)
	})
}

func TestLoadAuth_NoTokens(t *testing.T) {
	auth := LoadAuth("")
	assert.Empty(t, auth.GitHub)
	assert.Empty(t, auth.GitLab)
}

func TestAuthConfig_HasGitHubAuth(t *testing.T) {
	tests := []struct {
		name   string
		auth   AuthConfig
		expect bool
	}{
		{"with token", AuthConfig{GitHub: "token"}, true},
		{"empty token", AuthConfig{GitHub: ""}, false},
		{"only gitlab", AuthConfig{GitLab: "token"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.auth.HasGitHubAuth())
		})
	}
}

func TestAuthConfig_HasGitLabAuth(t *testing.T) {
	tests := []struct {
		name   string
		auth   AuthConfig
		expect bool
	}{
		{"with token", AuthConfig{GitLab: "token"}, true},
		{"empty token", AuthConfig{GitLab: ""}, false},
		{"only github", AuthConfig{GitHub: "token"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.auth.HasGitLabAuth())
		})
	}
}

func TestAuthConfig_TokenForForge(t *testing.T) {
	auth := AuthConfig{GitHub: "gh-token", GitLab: "gl-token"}

	tests := []struct {
		name   string
		forge  ForgeType
		expect string
	}{
		{"GitHub", ForgeGitHub, "gh-token"},
		{"GitLab", ForgeGitLab, "gl-token"},
		{"unknown forge", ForgeType("unknown"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, auth.TokenForForge(tt.forge))
		})
	}
}
