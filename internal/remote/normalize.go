package remote

import (
	"fmt"
	"strings"
)

// ToLocalRef converts any reference format to local name format.
// This is the canonical function for normalizing references before storage,
// lockfile operations, or comparisons.
//
// Input formats supported:
//   - Canonical HTTPS: https://github.com/owner/ctxloom-github@v1/bundles/core
//   - Canonical SSH: git@github.com:owner/repo@v1/bundles/core
//   - Canonical file: file:///path/to/repo@v1/bundles/core
//   - Simple: alice/core or alice/core@v1.0.0
//
// Output format: repoName/path (e.g., ctxloom-github/core)
//
// Examples:
//
//	https://github.com/owner/ctxloom-github@v1/bundles/core → ctxloom-github/core
//	git@github.com:owner/my-repo@v1/bundles/core → my-repo/core
//	alice/core@v1.0.0 → alice/core
//	alice/core → alice/core (unchanged)
func ToLocalRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty reference")
	}

	// Strip any item path suffix (e.g., #fragments/name) - preserve for later
	itemPath := ""
	if hashIdx := strings.Index(ref, "#"); hashIdx != -1 {
		itemPath = ref[hashIdx:]
		ref = ref[:hashIdx]
	}

	// Parse the reference
	parsed, err := ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("failed to parse reference: %w", err)
	}

	// Convert to local name
	localName := parsed.ToLocalName()

	// Re-append item path if present
	return localName + itemPath, nil
}

// IsCanonicalRef checks if a reference is in canonical URL format.
func IsCanonicalRef(ref string) bool {
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}
