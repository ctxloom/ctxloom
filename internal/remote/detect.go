package remote

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// NewFetcher creates a Fetcher appropriate for the given URL.
// Detects the forge type from the URL and returns the correct implementation.
func NewFetcher(repoURL string, auth AuthConfig) (Fetcher, error) {
	forgeType, _, err := DetectForge(repoURL)
	if err != nil {
		return nil, err
	}

	switch forgeType {
	case ForgeGitHub:
		return NewGitHubFetcher(auth.GitHub), nil
	case ForgeGitGeneric:
		return nil, fmt.Errorf("generic git forge has no API fetcher; use the clone-backed cache fetcher")
	default:
		return nil, fmt.Errorf("unsupported forge type: %s", forgeType)
	}
}

// NewForgeFetcher builds an API fetcher from a resolved forge: the github
// adapter at the forge's endpoint with a token read from its token_env, or an
// error for the generic git adapter (which has no API — reads go through the
// clone cache).
func NewForgeFetcher(repoURL string, rf ResolvedForge, auth AuthConfig) (Fetcher, error) {
	switch rf.Type {
	case ForgeGitHub:
		opts := []GitHubFetcherOption{}
		if rf.APIURL != "" {
			opts = append(opts, WithGitHubAPIURL(rf.APIURL))
		}
		return NewGitHubFetcher(rf.Token(auth), opts...), nil
	case ForgeGitGeneric:
		return nil, fmt.Errorf("generic git forge has no API fetcher; use the clone-backed cache fetcher")
	default:
		return nil, fmt.Errorf("unsupported forge type: %s", rf.Type)
	}
}

// Token returns the forge's token: the value of its token_env variable if set,
// falling back to the ambient auth token (GITHUB_TOKEN/GH_TOKEN). The generic
// git adapter holds no token here — its auth is ambient git.
//
// THIS FUNCTION IS NOT HOST-SCOPED, DELIBERATELY, AND YOU ARE NOT COVERED HERE.
// The fallback below returns the ambient github.com credential for a forge
// pointing at ANY host — an enterprise base_url with no token_env included —
// and rf.TokenEnv is itself defaulted to GITHUB_TOKEN for every github-typed
// forge regardless of host (resolvedFromConfig). The protection against
// spending a github.com credential somewhere else lives DOWNSTREAM, at the
// point a destination is actually known: RepoCache.cloneToken, feeding authEnv.
// That layering is the ruling, not an oversight — the value is read here, the
// decision to spend it is made where the host is.
//
// The cost is this: a NEW caller that takes Token's result to a network
// destination bypasses that scoping entirely. If you are writing one, scope it
// at your own boundary the way cloneToken does; do not assume this call did it.
func (rf ResolvedForge) Token(auth AuthConfig) string {
	if rf.Type != ForgeGitHub {
		return ""
	}
	if rf.TokenEnv != "" {
		if t := os.Getenv(rf.TokenEnv); t != "" {
			return t
		}
	}
	return auth.GitHub
}

// DetectForge determines the forge type from a repository URL.
// github.com (and shorthand owner/repo) resolve to the GitHub API adapter; any
// other host resolves to the generic git adapter, which clones the host's own
// endpoint and reads locally. The returned base URL is the forge endpoint.
func DetectForge(repoURL string) (ForgeType, string, error) {
	// Shorthand like "alice/ctxloom" implies GitHub.
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, ".") {
		return ForgeGitHub, "https://github.com", nil
	}

	// scp-style SSH ("git@host:owner/repo") is a spelling ParseRepoURL accepts
	// and NormalizeURL rewrites, but url.Parse refuses it outright — its first
	// path segment carries a colon — so the host is read off the string.
	if host, ok := scpLikeHost(repoURL); ok {
		return forgeForHost(host, "https://"+host)
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())

	// No authority component: url.Parse put the whole string in Path, so
	// joining Scheme and Host yields the literal "://", an endpoint that names
	// nothing. Two shapes land here — a host-qualified string with no scheme
	// ("example.com/owner/repo"), whose host is the first path segment; and a
	// path-addressed transport ("file:///srv/repo.git"), which has no host at
	// all and whose endpoint is the URL itself.
	if host == "" {
		seg, _, _ := strings.Cut(u.Path, "/")
		seg = strings.ToLower(seg)
		if u.Scheme == "" && strings.Contains(seg, ".") {
			return forgeForHost(seg, "https://"+seg)
		}
		return ForgeGitGeneric, repoURL, nil
	}

	// Any other host is consumed via the generic git adapter (clone + local
	// read) against its own endpoint.
	return forgeForHost(host, fmt.Sprintf("%s://%s", u.Scheme, u.Host))
}

// scpLikeHost returns the host of an scp-style SSH ref ("user@host:owner/repo")
// and whether the input had that shape: an "@" followed later by a ":", no
// scheme, and a dotted host. The host must be extracted textually because
// url.Parse rejects the whole form.
func scpLikeHost(repoURL string) (string, bool) {
	if strings.Contains(repoURL, "://") {
		return "", false
	}
	at := strings.Index(repoURL, "@")
	if at < 0 {
		return "", false
	}
	colon := strings.Index(repoURL[at:], ":")
	if colon < 0 {
		return "", false
	}
	host := strings.ToLower(repoURL[at+1 : at+colon])
	if !strings.Contains(host, ".") {
		return "", false
	}
	return host, true
}

// forgeForHost maps a resolved host to its adapter: github.com (with or without
// the www. label) to the GitHub API adapter at the canonical endpoint, every
// other host to the generic clone-backed adapter at base.
func forgeForHost(host, base string) (ForgeType, string, error) {
	if host == "github.com" || host == "www.github.com" {
		return ForgeGitHub, "https://github.com", nil
	}
	return ForgeGitGeneric, base, nil
}

// ParseOwnerRepo extracts owner and repo name from a URL or shorthand — the
// two segments a forge API path is built from.
// Supports:
//   - "alice/ctxloom" (shorthand)
//   - "https://github.com/alice/ctxloom"
//   - "git@github.com:alice/ctxloom.git"
//
// Both returned segments are non-empty on success: "alice/" and "/ctxloom" name
// no repository, and a caller handed "" would build a request path with a hole
// in it.
//
// This is the API-PATH renderer over the shared ParseRepoURL grammar
// (repourl.go). It was the fifth independent re-implementation of that grammar
// — it was named ParseRepoURL, and had its own shorthand arm, its own scp arm
// and its own .git handling, which is why "alice/ctxloom.git" used to yield the
// repo name "ctxloom.git" while every other consumer trimmed the suffix.
func ParseOwnerRepo(repoURL string) (owner, repo string, err error) {
	parsed, err := ParseRepoURL(repoURL)
	if err != nil {
		return "", "", err
	}
	return parsed.OwnerRepo()
}

// NormalizeURL renders a repository URL's IDENTITY: one https spelling per
// repository, whatever transport syntax it was written in. It is the input to
// trust.CanonicalRepoURL (the trust namespace key), to remotes.yaml lookups
// and to lockfile keys.
//
// The grammar itself lives in ParseRepoURL (repourl.go) — this is the identity
// renderer over it. It used to be a hand-rolled arm-per-shape function with a
// sibling, normalizeCloneURL, that had the same arms guarded differently; see
// repourl.go's header for what that cost.
//
// An empty input yields an empty string (it previously yielded "https://").
func NormalizeURL(repoURL string) string {
	parsed, err := ParseRepoURL(repoURL)
	if err != nil {
		return ""
	}
	return parsed.Normalized()
}
