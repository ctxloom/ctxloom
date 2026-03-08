package profiles

import (
	"fmt"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/remote"
)

// LockUpdate represents a lockfile entry that needs to be added/updated
// during profile import.
type LockUpdate struct {
	LocalName string
	ItemType  remote.ItemType
	Entry     remote.LockEntry
}

// TransformToCanonical converts a profile's local bundle references to canonical URLs.
// Used when exporting/publishing a profile for sharing.
//
// Each bundle reference is looked up in the lockfile to get the canonical URL.
// If a bundle is not in the lockfile, an error is returned.
//
// Example transformation:
//
//	Input:  "scm-github/core-practices"
//	Output: "https://github.com/owner/scm-github@v1/bundles/core-practices@v1.2.3"
func TransformToCanonical(profile *Profile, lockfile *remote.Lockfile) (*Profile, error) {
	transformed := *profile // Copy
	transformed.Bundles = make([]string, 0, len(profile.Bundles))

	for _, bundleRef := range profile.Bundles {
		ref := ParseContentRef(bundleRef)

		if ref.IsURL {
			// Already canonical, keep as-is
			transformed.Bundles = append(transformed.Bundles, bundleRef)
			continue
		}

		// Get the bundle path (local name without item specifier)
		localName := ref.BundlePath()

		// Look up in lockfile
		canonicalURL, found := lockfile.GetCanonicalURL(remote.ItemTypeBundle, localName)
		if !found {
			return nil, fmt.Errorf("bundle %q not found in lockfile; pull it first", localName)
		}

		// Add item path if present (e.g., #fragments/name)
		if ref.ItemType != "" {
			canonicalURL += "#" + ref.ItemType
			if ref.ItemName != "" {
				canonicalURL += "/" + ref.ItemName
			}
		}

		transformed.Bundles = append(transformed.Bundles, canonicalURL)
	}

	return &transformed, nil
}

// TransformToLocal converts canonical URLs in a profile to local names.
// Used when importing/pulling a profile.
//
// For each canonical URL:
// 1. Parse the URL to extract components
// 2. Find or derive a local remote name
// 3. Build the local name format
// 4. Return lockfile updates needed
//
// Example transformation:
//
//	Input:  "https://github.com/owner/scm-github@v1/bundles/core-practices@v1.2.3"
//	Output: "scm-github/core-practices"
//	Update: {LocalName: "scm-github/core-practices", Entry: {URL: ..., SCMVersion: "v1", ...}}
func TransformToLocal(profile *Profile, registry *remote.Registry, lockfile *remote.Lockfile) (*Profile, []LockUpdate, error) {
	transformed := *profile // Copy
	transformed.Bundles = make([]string, 0, len(profile.Bundles))

	var updates []LockUpdate

	for _, bundleRef := range profile.Bundles {
		ref := ParseContentRef(bundleRef)

		if !ref.IsURL {
			// Already local, keep as-is
			transformed.Bundles = append(transformed.Bundles, bundleRef)
			continue
		}

		// Parse the canonical URL to extract components
		parsed, err := remote.ParseReference(ref.Remote)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid canonical URL %q: %w", ref.Remote, err)
		}

		// Get or create a local remote for this URL
		localRemote, err := registry.GetOrCreateByURL(parsed.URL, parsed.Version)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to register remote for %q: %w", parsed.URL, err)
		}

		// Build local name: remoteName/bundlePath
		localName := fmt.Sprintf("%s/%s", localRemote.Name, parsed.Path)

		// Track for lockfile update
		updates = append(updates, LockUpdate{
			LocalName: localName,
			ItemType:  remote.ItemTypeBundle,
			Entry: remote.LockEntry{
				URL:              parsed.URL,
				SCMVersion:       parsed.Version,
				RequestedVersion: parsed.ContentVersion,
				// SHA will be filled in by the puller after fetching
			},
		})

		// Build local reference
		localRef := localName
		if ref.ItemType != "" {
			localRef += "#" + ref.ItemType
			if ref.ItemName != "" {
				localRef += "/" + ref.ItemName
			}
		}

		transformed.Bundles = append(transformed.Bundles, localRef)
	}

	return &transformed, updates, nil
}

// NeedsTransform checks if a profile has any canonical URLs that need transformation.
func NeedsTransform(profile *Profile) bool {
	for _, bundleRef := range profile.Bundles {
		if strings.HasPrefix(bundleRef, "https://") ||
			strings.HasPrefix(bundleRef, "http://") ||
			strings.HasPrefix(bundleRef, "git@") {
			return true
		}
	}
	return false
}

// HasLocalReferences checks if a profile has any local (non-URL) references.
func HasLocalReferences(profile *Profile) bool {
	for _, bundleRef := range profile.Bundles {
		if !strings.HasPrefix(bundleRef, "https://") &&
			!strings.HasPrefix(bundleRef, "http://") &&
			!strings.HasPrefix(bundleRef, "git@") {
			return true
		}
	}
	return false
}
