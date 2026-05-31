package remote

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// ParseReference parses a remote reference string.
//
// Supported formats:
//
// Simple (requires remotes.yaml lookup):
//   - "remote/path" → Remote="remote", Path="path"
//   - "remote/path@ref" → with ContentVersion
//   - "remote/nested/path@v1.0.0" → nested path with content version
//
// HTTPS URL (canonical, self-contained):
//   - "https://github.com/owner/repo@bundles/name"
//   - "https://gitlab.com/group/repo@fragments/security"
//
// SSH URL:
//   - "git@github.com:owner/repo@bundles/name"
//   - "git@gitlab.com:group/subgroup/repo@prompts/review"
//
// File URL (local repositories):
//   - "file:///path/to/repo@bundles/name"
//   - "file:///home/user/ctxloom-content@fragments/security"
func ParseReference(ref string) (*Reference, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty reference")
	}

	// Detect URL-based references
	if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
		return parseHTTPSReference(ref)
	}
	if strings.HasPrefix(ref, "git@") {
		return parseSSHReference(ref)
	}
	if strings.HasPrefix(ref, "file://") {
		return parseFileReference(ref)
	}

	// Simple format: remote/path[@ref]
	return parseSimpleReference(ref)
}

// parseSimpleReference parses the simple "remote/path[@ref]" format.
func parseSimpleReference(ref string) (*Reference, error) {
	// Split off content version if present
	contentVersion := ""
	if idx := strings.LastIndex(ref, "@"); idx != -1 {
		contentVersion = ref[idx+1:]
		ref = ref[:idx]
	}

	// Split into remote and path
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid reference format, expected 'remote/path': %s", ref)
	}

	remote := parts[0]
	itemPath := parts[1]

	if remote == "" {
		return nil, fmt.Errorf("empty remote name in reference")
	}
	if itemPath == "" {
		return nil, fmt.Errorf("empty path in reference")
	}

	return &Reference{
		Remote:         remote,
		Path:           itemPath,
		ContentVersion: contentVersion,
		IsCanonical:    false,
	}, nil
}

// parseHTTPSReference parses HTTPS URLs like:
//   - https://github.com/owner/repo@bundles/name (latest)
//   - https://github.com/owner/repo@bundles/name@v1.2.3 (pinned tag)
//   - https://github.com/owner/repo@bundles/name@abc123 (pinned SHA)
//
// Format: <repo_url>@<type>/<path>@<content_version>
func parseHTTPSReference(ref string) (*Reference, error) {
	// Split at @ to separate the repo URL from the item path
	// Format: https://github.com/owner/repo@type/path[@contentVersion]
	atIdx := strings.Index(ref, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}

	repoURL := ref[:atIdx]
	remainder := ref[atIdx+1:] // type/path[@contentVersion]

	// Parse the remainder: type/path[@contentVersion]
	itemType, itemPath, contentVersion, err := parseTypePathVersion(remainder)
	if err != nil {
		return nil, fmt.Errorf("invalid URL reference %s: %w", ref, err)
	}

	return &Reference{
		URL:            repoURL,
		ItemType:       itemType,
		Path:           itemPath,
		ContentVersion: contentVersion,
		IsCanonical:    true,
	}, nil
}

// parseSSHReference parses SSH URLs like:
//   - git@github.com:owner/repo@bundles/name (latest)
//   - git@github.com:owner/repo@bundles/name@v1.2.3 (pinned)
//
// Format: git@<host>:<path>@<type>/<path>@<content_version>
func parseSSHReference(ref string) (*Reference, error) {
	// SSH format: git@host:path@type/name[@contentVersion]
	// Find the @ that separates the item path (not the git@ prefix)

	// Skip "git@" prefix
	afterGit := ref[4:]

	// Find colon that separates host from path
	colonIdx := strings.Index(afterGit, ":")
	if colonIdx == -1 {
		return nil, fmt.Errorf("invalid SSH URL format: %s", ref)
	}

	hostPart := afterGit[:colonIdx]
	pathPart := afterGit[colonIdx+1:]

	// Find @ that separates repo from item path
	atIdx := strings.Index(pathPart, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("SSH URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}

	repoPath := pathPart[:atIdx]
	remainder := pathPart[atIdx+1:] // type/path[@contentVersion]

	// Reconstruct SSH URL without type/path
	repoURL := fmt.Sprintf("git@%s:%s", hostPart, repoPath)

	// Parse the remainder: type/path[@contentVersion]
	itemType, itemPath, contentVersion, err := parseTypePathVersion(remainder)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH URL reference %s: %w", ref, err)
	}

	return &Reference{
		URL:            repoURL,
		ItemType:       itemType,
		Path:           itemPath,
		ContentVersion: contentVersion,
		IsCanonical:    true,
	}, nil
}

// parseFileReference parses file:// URLs like:
//   - file:///path/to/repo@bundles/name (latest)
//   - file:///path/to/repo@bundles/name@v1.2.3 (pinned)
//
// Format: file://<path>@<type>/<path>@<content_version>
func parseFileReference(ref string) (*Reference, error) {
	// Parse as URL first
	u, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid file URL: %w", err)
	}

	// The path will contain repo@type/name[@contentVersion]
	fullPath := u.Path

	// Find @ that separates repo path from item path
	atIdx := strings.Index(fullPath, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("file URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}

	repoPath := fullPath[:atIdx]
	remainder := fullPath[atIdx+1:] // type/path[@contentVersion]

	// Reconstruct file URL without type/path
	repoURL := "file://" + repoPath

	// Parse the remainder: type/path[@contentVersion]
	itemType, itemPath, contentVersion, err := parseTypePathVersion(remainder)
	if err != nil {
		return nil, fmt.Errorf("invalid file URL reference %s: %w", ref, err)
	}

	return &Reference{
		URL:            repoURL,
		ItemType:       itemType,
		Path:           itemPath,
		ContentVersion: contentVersion,
		IsCanonical:    true,
	}, nil
}

// parseTypePathVersion parses "type/path[@contentVersion]" from a URL remainder.
// Examples:
//   - "bundles/core-practices" → bundles, core-practices, ""
//   - "bundles/core-practices@v1.2.3" → bundles, core-practices, "v1.2.3"
//   - "bundles/core-practices@abc123" → bundles, core-practices, "abc123"
func parseTypePathVersion(s string) (itemType ItemType, itemPath string, contentVersion string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("expected type/path, got: %s", s)
	}

	typeStr := parts[0]
	pathWithVersion := parts[1]

	// Check for content version suffix: path@contentVersion
	if atIdx := strings.LastIndex(pathWithVersion, "@"); atIdx != -1 {
		itemPath = pathWithVersion[:atIdx]
		contentVersion = pathWithVersion[atIdx+1:]
	} else {
		itemPath = pathWithVersion
		contentVersion = ""
	}

	if itemPath == "" {
		return "", "", "", fmt.Errorf("empty path")
	}

	// Parse item type (only bundles and profiles supported)
	switch typeStr {
	case "bundles":
		itemType = ItemTypeBundle
	case "profiles":
		itemType = ItemTypeProfile
	default:
		return "", "", "", fmt.Errorf("unknown item type: %s (only bundles and profiles supported)", typeStr)
	}

	return itemType, itemPath, contentVersion, nil
}

// String returns the string representation of a reference.
func (r *Reference) String() string {
	if r.IsCanonical {
		return r.CanonicalString()
	}
	if r.ContentVersion != "" {
		return fmt.Sprintf("%s/%s@%s", r.Remote, r.Path, r.ContentVersion)
	}
	return fmt.Sprintf("%s/%s", r.Remote, r.Path)
}

// CanonicalString returns the canonical URL representation.
func (r *Reference) CanonicalString() string {
	if r.URL == "" {
		return r.String()
	}
	typeName := r.ItemType.DirName()
	if typeName == "" {
		typeName = "bundles" // default
	}
	return fmt.Sprintf("%s@%s/%s", r.URL, typeName, r.Path)
}

// BuildFilePath constructs the path to the item within the repository.
// For canonical refs, uses the embedded item type.
// For simple refs, uses the provided itemType.
func (r *Reference) BuildFilePath(itemType ItemType) string {
	if r.IsCanonical {
		// Use embedded values
		return fmt.Sprintf("ctxloom/%s/%s.yaml", r.ItemType.DirName(), r.Path)
	}
	// ctxloom/bundles/go-tools.yaml
	return fmt.Sprintf("ctxloom/%s/%s.yaml", itemType.DirName(), r.Path)
}

// LocalPath returns the local path where the item would be installed.
// baseDir is the .ctxloom directory path.
// Bundles go in cache/bundles/, profiles go in profiles/ (at root).
func (r *Reference) LocalPath(baseDir string, itemType ItemType) string {
	var remoteName string

	if r.IsCanonical {
		itemType = r.ItemType
		remoteName = r.LocalRemoteName()
	} else {
		remoteName = r.Remote
	}

	switch itemType {
	case ItemTypeBundle:
		// Bundles: .ctxloom/cache/bundles/remote/path.yaml
		return fmt.Sprintf("%s/%s/%s/%s/%s.yaml", baseDir, paths.CacheDir, paths.BundlesDir, remoteName, r.Path)
	case ItemTypeProfile:
		// Profiles: .ctxloom/profiles/remote/path.yaml (at root level, no cache layer)
		return fmt.Sprintf("%s/%s/%s/%s.yaml", baseDir, paths.ProfilesDir, remoteName, r.Path)
	default:
		// Default to bundles
		return fmt.Sprintf("%s/%s/%s/%s/%s.yaml", baseDir, paths.CacheDir, paths.BundlesDir, remoteName, r.Path)
	}
}

// LocalRemoteName returns a filesystem-safe name for the remote.
// For canonical URLs, this extracts a meaningful identifier.
func (r *Reference) LocalRemoteName() string {
	if r.URL == "" {
		return r.Remote
	}

	// Extract a meaningful name from the URL:
	//   https://github.com/owner/repo → github.com/owner/repo
	//   git@github.com:owner/repo     → github.com/owner/repo
	//   file:///path/to/repo          → path/to/repo
	switch {
	case strings.HasPrefix(r.URL, "https://"), strings.HasPrefix(r.URL, "http://"):
		return httpHostPath(r.URL)
	case strings.HasPrefix(r.URL, "git@"):
		if name, ok := sshHostPath(r.URL); ok {
			return name
		}
	case strings.HasPrefix(r.URL, "file://"):
		if name, ok := fileLastTwoComponents(r.URL); ok {
			return name
		}
	}

	return sanitizePath(r.URL)
}

// httpHostPath returns host/path for an http(s) URL, falling back to a
// sanitized form when the URL won't parse.
func httpHostPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return sanitizePath(rawURL)
	}
	return path.Join(u.Host, u.Path)
}

// sshHostPath returns host/path for a git@host:owner/repo URL, reporting ok when
// it matched the SSH shape.
func sshHostPath(rawURL string) (string, bool) {
	re := regexp.MustCompile(`^git@([^:]+):(.+)$`)
	if matches := re.FindStringSubmatch(rawURL); len(matches) == 3 {
		return path.Join(matches[1], matches[2]), true
	}
	return "", false
}

// fileLastTwoComponents returns the last two path components of a file:// URL
// (for uniqueness), reporting ok when the path had usable components.
func fileLastTwoComponents(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return sanitizePath(rawURL), true
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		return path.Join(parts[len(parts)-2], parts[len(parts)-1]), true
	}
	if len(parts) == 1 {
		return parts[0], true
	}
	return "", false
}

// RepoURL returns the repository URL for fetching.
// For canonical refs, returns the embedded URL.
// For simple refs, returns empty (caller must look up in registry).
func (r *Reference) RepoURL() string {
	return r.URL
}

// sanitizePath makes a string safe for use in file paths.
func sanitizePath(s string) string {
	// Remove/replace problematic characters
	s = strings.ReplaceAll(s, "://", "/")
	s = strings.ReplaceAll(s, ":", "/")
	s = strings.ReplaceAll(s, "@", "/")
	return s
}

// ToCanonical converts a simple reference to a canonical URL reference.
// Requires looking up the remote in the registry to get the URL.
// itemType specifies what type of item this reference points to.
func (r *Reference) ToCanonical(registry *Registry, itemType ItemType) (*Reference, error) {
	if r.IsCanonical {
		return r, nil // Already canonical
	}

	// Look up remote in registry
	remote, err := registry.Get(r.Remote)
	if err != nil {
		return nil, fmt.Errorf("cannot convert to canonical: %w", err)
	}

	return &Reference{
		URL:            remote.URL,
		ItemType:       itemType,
		Path:           r.Path,
		ContentVersion: r.ContentVersion,
		IsCanonical:    true,
	}, nil
}

// MustCanonical converts to canonical, panicking on error.
// Only use when you're certain the remote exists.
func (r *Reference) MustCanonical(registry *Registry, itemType ItemType) *Reference {
	canonical, err := r.ToCanonical(registry, itemType)
	if err != nil {
		panic(err)
	}
	return canonical
}

// ToLocalName converts a canonical reference to local name format.
// The local name uses the repository name as the remote alias.
//
// Examples:
//
//	https://github.com/owner/ctxloom-github@bundles/core-practices@v1.2.3
//	  -> ctxloom-github/core-practices
//
//	git@github.com:owner/my-repo@profiles/dev@abc123
//	  -> my-repo/dev
//
// For simple (non-canonical) references, returns Remote/Path as-is.
func (r *Reference) ToLocalName() string {
	if !r.IsCanonical {
		// Already a local format
		return fmt.Sprintf("%s/%s", r.Remote, r.Path)
	}

	repoName := ExtractRepoName(r.URL)
	return fmt.Sprintf("%s/%s", repoName, r.Path)
}

// ExtractRepoName extracts the repository name from a URL.
//
// Examples:
//
//	https://github.com/owner/repo -> repo
//	https://github.com/owner/my-ctxloom-content -> my-ctxloom-content
//	git@github.com:owner/repo -> repo
//	file:///path/to/repo -> repo
func ExtractRepoName(repoURL string) string {
	switch {
	case strings.HasPrefix(repoURL, "https://"), strings.HasPrefix(repoURL, "http://"):
		return lastURLPathComponent(repoURL)
	case strings.HasPrefix(repoURL, "git@"):
		return sshRepoName(repoURL)
	case strings.HasPrefix(repoURL, "file://"):
		return lastURLPathComponent(repoURL)
	}
	return sanitizePath(repoURL)
}

// lastURLPathComponent returns the final path component of an http(s)/file URL
// (the repo name), falling back to a sanitized form on parse failure.
func lastURLPathComponent(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return sanitizePath(repoURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return sanitizePath(repoURL)
}

// sshRepoName returns the repo name from a git@host:owner/repo URL.
func sshRepoName(repoURL string) string {
	re := regexp.MustCompile(`^git@[^:]+:(.+)$`)
	if matches := re.FindStringSubmatch(repoURL); len(matches) == 2 {
		parts := strings.Split(matches[1], "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return sanitizePath(repoURL)
}

// ToCanonicalWithVersion builds the full canonical URL string including content version.
// Used when exporting profiles for sharing.
//
// Format: <repo_url>@<type>/<path>@<content_version>
//
// If ContentVersion is empty, the @<content_version> suffix is omitted.
func (r *Reference) ToCanonicalWithVersion() string {
	if r.URL == "" {
		return r.String()
	}

	typeName := r.ItemType.DirName()
	if typeName == "" {
		typeName = "bundles" // default
	}

	base := fmt.Sprintf("%s@%s/%s", r.URL, typeName, r.Path)

	if r.ContentVersion != "" {
		return fmt.Sprintf("%s@%s", base, r.ContentVersion)
	}
	return base
}

// EffectiveContentVersion returns the content version to use for fetching.
// Returns empty string if no version is specified (use HEAD/latest).
func (r *Reference) EffectiveContentVersion() string {
	return r.ContentVersion
}
