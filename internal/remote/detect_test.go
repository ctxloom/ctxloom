package remote

import (
	"strings"
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

		// Any non-github host resolves to the generic git adapter at its own endpoint
		{"gitlab.com https", "https://gitlab.com/owner/repo", ForgeGitGeneric, "https://gitlab.com", false},
		{"www.gitlab.com https", "https://www.gitlab.com/owner/repo", ForgeGitGeneric, "https://www.gitlab.com", false},
		{"self-hosted gitlab", "https://gitlab.company.com/owner/repo", ForgeGitGeneric, "https://gitlab.company.com", false},
		{"self-hosted my-gitlab", "https://my-gitlab.internal.org/group/project", ForgeGitGeneric, "https://my-gitlab.internal.org", false},
		{"unknown host", "https://unknown.host.com/owner/repo", ForgeGitGeneric, "https://unknown.host.com", false},

		// scp-style SSH refs. url.Parse rejects them outright ("first path
		// segment in URL cannot contain colon"), so the host has to be taken by
		// hand — but ParseRepoURL documents and accepts exactly this form, and
		// NormalizeURL rewrites it to https, so DetectForge refusing it made one
		// package disagree with itself about which spellings name a repository.
		{"scp ssh github", "git@github.com:owner/repo.git", ForgeGitHub, "https://github.com", false},
		{"scp ssh github no suffix", "git@github.com:owner/repo", ForgeGitHub, "https://github.com", false},
		{"scp ssh self-hosted", "git@gitlab.company.com:group/project.git", ForgeGitGeneric, "https://gitlab.company.com", false},

		// Host-less input. url.Parse puts the whole string in Path when there is
		// no authority component, so "%s://%s" over (Scheme, Host) rendered the
		// literal "://" — a base URL that names nothing at all. The endpoint
		// must be something a reader could act on: the host when one can be
		// read off the path, otherwise the input itself.
		{"bare host, no scheme, no path", "gitlab.com", ForgeGitGeneric, "https://gitlab.com", false},
		{"bare host-qualified path", "gitlab.com/owner/repo", ForgeGitGeneric, "https://gitlab.com", false},
		{"bare github.com host", "github.com/owner/repo", ForgeGitHub, "https://github.com", false},
		{"path-addressed transport", "file:///srv/repo.git", ForgeGitGeneric, "file:///srv/repo.git", false},
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

func TestParseOwnerRepo(t *testing.T) {
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

		// An empty owner or repo segment is not a repository. Reporting success
		// hands the caller "" and it goes on to build /repos//<repo> (or
		// /repos/<owner>/) and blame the forge for the 404.
		{"shorthand empty repo", "alice/", "", "", true},
		{"shorthand empty owner", "/repo", "", "", true},
		{"shorthand both empty", "/", "", "", true},
		{"ssh empty repo", "git@github.com:alice/", "", "", true},
		{"ssh empty owner", "git@github.com:/repo", "", "", true},
		{"https empty owner", "https://github.com//repo", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseOwnerRepo(tt.url)
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
		{"ssh with .git", "git@github.com:owner/repo.git", "https://github.com/owner/repo.git"},
		{"ssh gitlab", "git@gitlab.com:group/project.git", "https://gitlab.com/group/project.git"},

		// HTTPS URLs
		{"https already good", "https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"https with .git", "https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"http gets kept", "http://github.com/owner/repo", "http://github.com/owner/repo"},

		// No scheme but host-qualified: the first segment carries a ".", so it
		// is a HOSTNAME, not a GitHub owner (GitHub owner names cannot contain
		// one). This used to be pinned in the opposite direction, under the
		// name "domain no scheme gets double-prefixed", because NormalizeURL's
		// shorthand arm had no guard and sent it to
		// "https://github.com/gitlab.com/alice/repo" — a repository that
		// cannot exist. The human ruled on it: standardize on the correct
		// answer.
		//
		// It is the one input class in this suite whose trust-namespace key
		// MOVES. Nobody can hold an approval under the old key, because no
		// content was ever fetchable from it.
		{"host-qualified, scheme omitted", "github.com/owner/repo", "https://github.com/owner/repo"},
		{"host-qualified other forge", "gitlab.com/alice/repo", "https://gitlab.com/alice/repo"},
		{"host-qualified with .git", "gitlab.com/alice/repo.git", "https://gitlab.com/alice/repo.git"},

		// ...while a dot in a LATER segment leaves it shorthand. The sibling
		// normalizeCloneURL rejected shorthand on a dot ANYWHERE, so this one
		// came back bare and `git clone` read it as a local directory.
		{"shorthand with a dotted repo name", "owner/repo.js", "https://github.com/owner/repo.js"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeURL(tt.input))
		})
	}
}

// TestNormalizeURL_FinalHTTPSFallbackIsReachable pins the reachability of
// NormalizeURL's last return — "No scheme, not shorthand, not scp-like: assume
// an HTTPS host."
//
// A prior version of this suite asserted that arm is unreachable. It is not:
// an input has to clear three gates to arrive there — no "://", no "@", and
// NO "/" — and a bare host name clears all three. What IS true is the row's
// first clause, that a
// host-qualified path ("gitlab.com/owner/repo") is stolen by the shorthand arm
// above and prefixed onto github.com; that half is escalated separately because
// NormalizeURL feeds trust.CanonicalRepoURL, so changing it changes which trust
// namespace a repository is consulted under.
//
// If someone later "removes the unreachable fallback", this goes red.
func TestNormalizeURL_FinalHTTPSFallbackIsReachable(t *testing.T) {
	for _, input := range []string{"gitlab.com", "git.company.internal", "example.com.git"} {
		got := NormalizeURL(input)
		assert.True(t, strings.HasPrefix(got, "https://"),
			"NormalizeURL(%q) = %q: the final https:// fallback is the only arm that can produce this", input, got)
		assert.NotContains(t, got, "github.com",
			"NormalizeURL(%q) = %q: a bare host must not be routed through the github shorthand arm", input, got)
	}
	assert.Equal(t, "https://gitlab.com", NormalizeURL("gitlab.com"))
	// A bare host is not a path, so there is no ".git" suffix to speak of here:
	// "example.com.git" is a HOST NAME. It is preserved for the same reason
	// every other spelling is — nothing here knows it is not a real host.
	assert.Equal(t, "https://example.com.git", NormalizeURL("example.com.git"))
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

	t.Run("rejects generic git host (no API fetcher)", func(t *testing.T) {
		_, err := NewFetcher("https://gitlab.com/owner/repo", AuthConfig{})
		require.Error(t, err)
	})

	t.Run("rejects self-hosted generic git host", func(t *testing.T) {
		_, err := NewFetcher("https://git.company.com/owner/repo", AuthConfig{})
		require.Error(t, err)
	})

	t.Run("uses auth token", func(t *testing.T) {
		auth := AuthConfig{GitHub: "gh-token"}

		ghFetcher, err := NewFetcher("https://github.com/owner/repo", auth)
		require.NoError(t, err)
		assert.Equal(t, ForgeGitHub, ghFetcher.Forge())
	})
}
