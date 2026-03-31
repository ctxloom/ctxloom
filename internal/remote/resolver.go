// Package remote provides functionality for managing remote fragment/prompt sources.
package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// BundleResolver resolves bundle URL references to local paths.
// It uses the lockfile and remotes configuration to map URLs to local cache paths.
type BundleResolver struct {
	appDir          string
	lockfileManager *LockfileManager
	remoteConfig    *RemoteConfig
	fs              afero.Fs
}

// ResolverOption is a functional option for configuring a BundleResolver.
type ResolverOption func(*BundleResolver)

// WithResolverFS sets a custom filesystem implementation (for testing).
func WithResolverFS(fs afero.Fs) ResolverOption {
	return func(r *BundleResolver) {
		r.fs = fs
	}
}

// NewBundleResolver creates a new bundle resolver.
func NewBundleResolver(appDir string, opts ...ResolverOption) *BundleResolver {
	if appDir == "" {
		appDir = ".ctxloom"
	}
	r := &BundleResolver{
		appDir:          appDir,
		lockfileManager: NewLockfileManager(appDir),
		fs:              afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithRemoteConfig sets the remote configuration for alias resolution.
func (r *BundleResolver) WithRemoteConfig(cfg *RemoteConfig) *BundleResolver {
	r.remoteConfig = cfg
	return r
}

// ResolveToLocalPath resolves a bundle reference (URL or alias) to its local filesystem path.
// Returns the local path if the bundle is cached locally, or an error if not found.
//
// Supported formats:
//   - https://github.com/owner/repo@v1/bundles/name
//   - git@github.com:owner/repo@v1/bundles/name
//   - github/name (alias format, requires remotes.yaml)
func (r *BundleResolver) ResolveToLocalPath(bundleRef string) (string, error) {
	// Try to parse as a canonical URL reference
	ref, err := ParseReference(bundleRef)
	if err != nil {
		// Not a valid reference format
		return "", fmt.Errorf("invalid bundle reference %q: %w", bundleRef, err)
	}

	// Get the local path using the reference
	localPath := ref.LocalPath(r.appDir, ItemTypeBundle)

	// Check if the file exists
	if _, err := r.fs.Stat(localPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("bundle not cached locally: %s (expected at %s)", bundleRef, localPath)
		}
		return "", fmt.Errorf("error checking bundle path: %w", err)
	}

	return localPath, nil
}

// ResolvedBundle contains a resolved bundle reference with both local and canonical paths.
type ResolvedBundle struct {
	// OriginalRef is the reference as specified in the profile
	OriginalRef string

	// LocalPath is the resolved local filesystem path
	LocalPath string

	// CanonicalURL is the canonical URL form (for export)
	CanonicalURL string

	// LockEntry contains provenance info if found in lockfile
	LockEntry *LockEntry
}

// ResolveBundle resolves a bundle reference and returns full resolution info.
func (r *BundleResolver) ResolveBundle(bundleRef string) (*ResolvedBundle, error) {
	result := &ResolvedBundle{
		OriginalRef: bundleRef,
	}

	// Parse the reference
	ref, err := ParseReference(bundleRef)
	if err != nil {
		return nil, fmt.Errorf("invalid bundle reference %q: %w", bundleRef, err)
	}

	// Get local path
	result.LocalPath = ref.LocalPath(r.appDir, ItemTypeBundle)

	// Check if file exists
	if _, err := r.fs.Stat(result.LocalPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("bundle not cached locally: %s (expected at %s)", bundleRef, result.LocalPath)
		}
		return nil, fmt.Errorf("error checking bundle path: %w", err)
	}

	// Set canonical URL
	if ref.IsCanonical {
		result.CanonicalURL = bundleRef
	} else {
		// For alias-based refs, construct the canonical URL
		result.CanonicalURL = ref.CanonicalString()
	}

	// Look up in lockfile for provenance
	lockfile, err := r.lockfileManager.Load()
	if err == nil {
		if entry, ok := lockfile.GetEntry(ItemTypeBundle, bundleRef); ok {
			result.LockEntry = &entry
		}
	}

	return result, nil
}

// LocalPathToCanonicalURL converts a local bundle path back to its canonical URL.
// This is used for export scenarios where we need to share a profile with absolute URLs.
func (r *BundleResolver) LocalPathToCanonicalURL(localPath string) (string, error) {
	// Load lockfile to find the entry
	lockfile, err := r.lockfileManager.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load lockfile: %w", err)
	}

	// Search through all bundle entries to find one matching this local path
	for ref, entry := range lockfile.Bundles {
		// Parse the ref to get what local path it would resolve to
		parsed, err := ParseReference(ref)
		if err != nil {
			continue
		}

		expectedPath := parsed.LocalPath(r.appDir, ItemTypeBundle)
		if expectedPath == localPath || filepath.Clean(expectedPath) == filepath.Clean(localPath) {
			// Found it - construct canonical URL
			if parsed.IsCanonical {
				return ref, nil
			}
			// For alias refs, build canonical URL from entry metadata
			return fmt.Sprintf("%s@%s/bundles/%s", entry.URL, entry.SCMVersion, parsed.Path), nil
		}
	}

	return "", fmt.Errorf("no lockfile entry found for local path: %s", localPath)
}

// ExtractBundleName extracts the bundle name from a local path.
// e.g., ".ctxloom/bundles/github.com/owner/repo/core-practices.yaml" -> "core-practices"
func ExtractBundleName(localPath string) string {
	base := filepath.Base(localPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// IsURLReference returns true if the reference appears to be a URL-based reference.
func IsURLReference(ref string) bool {
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}
