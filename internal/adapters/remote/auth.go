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
