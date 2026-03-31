package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectForge(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectForge ForgeType
		expectBase  string
		expectErr   bool
	}{
		// Shorthand
		{"shorthand alice/ctxloom", "alice/ctxloom", ForgeGitHub, "https://github.com", false},
		{"shorthand owner/repo-name", "owner/repo-name", ForgeGitHub, "https://github.com", false},

		// GitHub URLs
		{"github.com https", "https://github.com/owner/repo", ForgeGitHub, "https://github.com", false},
		{"www.github.com https", "https://www.github.com/owner/repo", ForgeGitHub, "https://github.com", false},
		{"github.com http", "http://github.com/owner/repo", ForgeGitHub, "https://github.com", false},

		// GitLab URLs
		{"gitlab.com https", "https://gitlab.com/owner/repo", ForgeGitLab, "https://gitlab.com", false},
		{"www.gitlab.com https", "https://www.gitlab.com/owner/repo", ForgeGitLab, "https://gitlab.com", false},
		{"self-hosted gitlab", "https://gitlab.company.com/owner/repo", ForgeGitLab, "https://gitlab.company.com", false},
		{"self-hosted my-gitlab", "https://my-gitlab.internal.org/group/project", ForgeGitLab, "https://my-gitlab.internal.org", false},

		// Unknown hosts default to GitHub
		{"unknown host", "https://unknown.host.com/owner/repo", ForgeGitHub, "https://github.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forge, baseURL, err := DetectForge(tt.url)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectForge, forge)
			assert.Equal(t, tt.expectBase, baseURL)
		})
	}
}

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectOwner string
		expectRepo  string
		expectErr   bool
	}{
		// Shorthand
		{"shorthand simple", "alice/ctxloom", "alice", "ctxloom", false},
		{"shorthand with dash", "my-org/my-repo", "my-org", "my-repo", false},

		// HTTPS URLs
		{"https github", "https://github.com/owner/repo", "owner", "repo", false},
		{"https gitlab", "https://gitlab.com/org/project", "org", "project", false},
		{"https with .git", "https://github.com/owner/repo.git", "owner", "repo", false},
		{"https with trailing slash", "https://github.com/owner/repo/", "owner", "repo", false},

		// SSH URLs
		{"ssh github", "git@github.com:owner/repo", "owner", "repo", false},
		{"ssh with .git", "git@github.com:owner/repo.git", "owner", "repo", false},
		{"ssh gitlab", "git@gitlab.com:group/project.git", "group", "project", false},

		// Edge cases
		{"nested groups", "https://gitlab.com/group/subgroup/project", "group", "subgroup", false},

		// Errors
		{"invalid shorthand", "justarepo", "", "", true},
		{"empty path", "https://github.com/", "", "", true},
		{"single segment", "https://github.com/owner", "", "", true},
		{"invalid ssh no colon", "git@github.comownerrepo", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseRepoURL(tt.url)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectOwner, owner)
			assert.Equal(t, tt.expectRepo, repo)
		})
	}
}

func TestExpandShorthand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple shorthand", "alice/ctxloom", "https://github.com/alice/ctxloom"},
		{"already full URL", "https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"gitlab URL", "https://gitlab.com/owner/repo", "https://gitlab.com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExpandShorthand(tt.input))
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Shorthand
		{"shorthand", "owner/repo", "https://github.com/owner/repo"},
		{"shorthand with dash", "my-org/my-repo", "https://github.com/my-org/my-repo"},

		// SSH URLs
		{"ssh github", "git@github.com:owner/repo", "https://github.com/owner/repo"},
		{"ssh with .git", "git@github.com:owner/repo.git", "https://github.com/owner/repo"},
		{"ssh gitlab", "git@gitlab.com:group/project.git", "https://gitlab.com/group/project"},

		// HTTPS URLs
		{"https already good", "https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"https with .git", "https://github.com/owner/repo.git", "https://github.com/owner/repo"},
		{"http gets kept", "http://github.com/owner/repo", "http://github.com/owner/repo"},

		// No scheme (but has domain) - treated as shorthand owner/repo
		{"domain no scheme gets double-prefixed", "github.com/owner/repo", "https://github.com/github.com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeURL(tt.input))
		})
	}
}

func TestNewFetcher(t *testing.T) {
	t.Run("creates GitHub fetcher for GitHub URL", func(t *testing.T) {
		fetcher, err := NewFetcher("https://github.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, fetcher.Forge())
	})

	t.Run("creates GitHub fetcher for shorthand", func(t *testing.T) {
		fetcher, err := NewFetcher("owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, fetcher.Forge())
	})

	t.Run("creates GitLab fetcher for GitLab URL", func(t *testing.T) {
		fetcher, err := NewFetcher("https://gitlab.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitLab, fetcher.Forge())
	})

	t.Run("creates GitLab fetcher for self-hosted GitLab", func(t *testing.T) {
		fetcher, err := NewFetcher("https://gitlab.company.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.Equal(t, ForgeGitLab, fetcher.Forge())
	})

	t.Run("uses auth tokens", func(t *testing.T) {
		auth := AuthConfig{
			GitHub: "gh-token",
			GitLab: "gl-token",
		}

		ghFetcher, err := NewFetcher("https://github.com/owner/repo", auth)
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, ghFetcher.Forge())

		glFetcher, err := NewFetcher("https://gitlab.com/owner/repo", auth)
		require.NoError(t, err)
		assert.Equal(t, ForgeGitLab, glFetcher.Forge())
	})
}
