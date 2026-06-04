package remote

import (
	"os"
)

// LoadAuth loads authentication from environment variables.
// Supported variables:
//   - GITHUB_TOKEN or GH_TOKEN for GitHub
func LoadAuth(configPath string) AuthConfig {
	auth := AuthConfig{}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		auth.GitHub = token
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		auth.GitHub = token
	}

	return auth
}

// HasGitHubAuth returns true if GitHub authentication is configured.
func (a AuthConfig) HasGitHubAuth() bool {
	return a.GitHub != ""
}

// TokenForForge returns the authentication token for the given forge.
// The generic git adapter uses ambient git auth and has no token here.
func (a AuthConfig) TokenForForge(forge ForgeType) string {
	switch forge {
	case ForgeGitHub:
		return a.GitHub
	default:
		return ""
	}
}
