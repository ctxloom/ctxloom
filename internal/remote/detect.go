package remote

import (
	"fmt"
	"net/url"
	"strings"
)

// NewFetcher creates a Fetcher appropriate for the given URL.
// Detects the forge type from the URL and returns the correct implementation.
func NewFetcher(repoURL string, auth AuthConfig) (Fetcher, error) {
	forgeType, baseURL, err := DetectForge(repoURL)
	if err != nil {
		return nil, err
	}

	switch forgeType {
	case ForgeGitHub:
		return NewGitHubFetcher(auth.GitHub), nil
	case ForgeGitLab:
		return NewGitLabFetcher(baseURL, auth.GitLab)
	default:
		return nil, fmt.Errorf("unsupported forge type: %s", forgeType)
	}
}

// DetectForge determines the forge type from a repository URL.
// Returns the forge type and the base URL for the forge.
func DetectForge(repoURL string) (ForgeType, string, error) {
	// Handle shorthand notation (e.g., "alice/ctxloom" -> GitHub)
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, ".") {
		// Shorthand like "alice/ctxloom" implies GitHub
		return ForgeGitHub, "https://github.com", nil
	}

	// Parse as URL
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())

	// GitHub
	if host == "github.com" || host == "www.github.com" {
		return ForgeGitHub, "https://github.com", nil
	}

	// GitLab.com
	if host == "gitlab.com" || host == "www.gitlab.com" {
		return ForgeGitLab, "https://gitlab.com", nil
	}

	// Self-hosted GitLab (check for "gitlab" in hostname)
	if strings.Contains(host, "gitlab") {
		baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		return ForgeGitLab, baseURL, nil
	}

	// Default to GitHub for unknown hosts (common for enterprise)
	// Users can explicitly configure if this is wrong
	return ForgeGitHub, "https://github.com", nil
}

// ParseRepoURL extracts owner and repo name from a URL or shorthand.
// Supports:
//   - "alice/ctxloom" (shorthand)
//   - "https://github.com/alice/ctxloom"
//   - "https://gitlab.com/alice/ctxloom"
//   - "git@github.com:alice/ctxloom.git"
func ParseRepoURL(repoURL string) (owner, repo string, err error) {
	// Handle shorthand notation
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
		parts := strings.Split(repoURL, "/")
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("invalid shorthand format, expected 'owner/repo': %s", repoURL)
	}

	// Handle SSH URLs (git@github.com:owner/repo.git)
	if strings.HasPrefix(repoURL, "git@") {
		// git@github.com:owner/repo.git -> owner/repo
		idx := strings.Index(repoURL, ":")
		if idx == -1 {
			return "", "", fmt.Errorf("invalid SSH URL format: %s", repoURL)
		}
		path := repoURL[idx+1:]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("invalid SSH URL path: %s", repoURL)
	}

	// Handle HTTPS URLs
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}

	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		return "", "", fmt.Errorf("URL path must contain owner/repo: %s", repoURL)
	}

	return parts[0], parts[1], nil
}

// ExpandShorthand converts a shorthand reference to a full URL.
// "alice/ctxloom" -> "https://github.com/alice/ctxloom"
func ExpandShorthand(shorthand string) string {
	if strings.Contains(shorthand, "://") {
		return shorthand // Already a full URL
	}
	return "https://github.com/" + shorthand
}

// NormalizeURL ensures a URL has a scheme and removes trailing .git.
func NormalizeURL(repoURL string) string {
	// Handle shorthand
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
		if strings.Contains(repoURL, "/") {
			return "https://github.com/" + repoURL
		}
	}

	// Handle SSH URLs - convert to HTTPS
	if strings.HasPrefix(repoURL, "git@") {
		// git@github.com:owner/repo.git -> https://github.com/owner/repo
		repoURL = strings.TrimPrefix(repoURL, "git@")
		repoURL = strings.Replace(repoURL, ":", "/", 1)
		repoURL = "https://" + repoURL
	}

	// Remove .git suffix
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Ensure scheme
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		repoURL = "https://" + repoURL
	}

	return repoURL
}
