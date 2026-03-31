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

// ToCanonicalRef converts a local reference to canonical URL format.
// This is used when exporting profiles for sharing or publishing.
//
// Input format: remoteName/path (e.g., ctxloom-github/core)
// Output format: https://github.com/owner/repo@version/bundles/path
//
// Requires registry lookup to get the URL for the remote name.
//
// Examples:
//
//	ctxloom-github/core → https://github.com/owner/ctxloom-github@v1/bundles/core
//	alice/security → https://github.com/alice/ctxloom@v1/bundles/security
func ToCanonicalRef(ref string, registry *Registry, itemType ItemType) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty reference")
	}

	// Strip any item path suffix (e.g., #fragments/name)
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

	// If already canonical, just return it (with item path)
	if parsed.IsCanonical {
		return parsed.ToCanonicalWithVersion() + itemPath, nil
	}

	// Convert to canonical using registry
	canonical, err := parsed.ToCanonical(registry, itemType)
	if err != nil {
		return "", fmt.Errorf("failed to convert to canonical: %w", err)
	}

	return canonical.ToCanonicalWithVersion() + itemPath, nil
}

// NormalizeProfileBundles normalizes all bundle references in a profile to local format.
// This should be called when pulling a profile to ensure consistent storage.
func NormalizeProfileBundles(bundles []string) ([]string, error) {
	normalized := make([]string, 0, len(bundles))

	for _, bundle := range bundles {
		if bundle == "" {
			continue
		}

		local, err := ToLocalRef(bundle)
		if err != nil {
			// Return error with context about which bundle failed
			return nil, fmt.Errorf("failed to normalize bundle %q: %w", bundle, err)
		}

		normalized = append(normalized, local)
	}

	return normalized, nil
}

// IsCanonicalRef checks if a reference is in canonical URL format.
func IsCanonicalRef(ref string) bool {
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}

// IsLocalRef checks if a reference is in local format (not a URL).
func IsLocalRef(ref string) bool {
	return !IsCanonicalRef(ref)
}
