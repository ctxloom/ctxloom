package remote

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// LocalSource is the fixed source token for ctxloom:local references —
// project-authored content under the committed .ctxloom/local/ working copy.
// It mirrors the canonical grammar: LocalSource @ <type>/<path>[@version].
const LocalSource = "ctxloom:local"

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
//   - "https://git.example.com/group/repo@fragments/security"
//
// SSH URL:
//   - "git@github.com:owner/repo@bundles/name"
//   - "git@git.example.com:group/subgroup/repo@prompts/review"
//
// File URL (local repositories):
//   - "file:///path/to/repo@bundles/name"
//   - "file:///home/user/ctxloom-content@fragments/security"
//
// Local source (project-authored, committed .ctxloom/local/):
//   - "ctxloom:local@bundles/name"
//   - "ctxloom:local@profiles/dev@<rev>" (pinned to a project revision)
func ParseReference(ref string) (*Reference, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty reference")
	}

	// Local source: ctxloom:local@<type>/<path>[@version]
	if strings.HasPrefix(ref, LocalSource+"@") {
		return parseLocalReference(ref)
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

	// No recognized scheme: the short "repo/path" form has been eliminated.
	// References must be scheme-qualified — a canonical URL (https://, git@,
	// file://) or a local ref (ctxloom:local@...).
	return nil, fmt.Errorf("unsupported reference %q: use a canonical URL "+
		"(e.g. https://github.com/owner/repo@bundles/name) or ctxloom:local@bundles/name "+
		"— the short \"repo/path\" form is no longer accepted", ref)
}

// parseLocalReference parses ctxloom:local references like:
//   - ctxloom:local@bundles/name (current working copy)
//   - ctxloom:local@profiles/dev@<rev> (pinned to a project revision)
//
// Format: ctxloom:local@<type>/<path>[@version]. The tail after the source
// token is parsed identically to a canonical URL's (parseTypePathVersion), so
// the local and remote grammars stay in lockstep. The version is opaque and
// usually empty — local content's version is the surrounding project's own VCS
// state.
func parseLocalReference(ref string) (*Reference, error) {
	// Strip the source token; the "@" was matched by the caller.
	remainder := strings.TrimPrefix(ref, LocalSource+"@") // type/path[@version]

	itemType, itemPath, contentVersion, err := parseTypePathVersion(remainder)
	if err != nil {
		return nil, fmt.Errorf("invalid local reference %s: %w", ref, err)
	}

	return &Reference{
		ItemType:       itemType,
		Path:           itemPath,
		ContentVersion: contentVersion,
		IsLocal:        true,
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
	}, nil
}

// parseTypePathVersion parses "type/path[@contentVersion]" from a URL remainder.
// Examples:
//   - "bundles/core-practices" → bundles, core-practices, ""
//   - "bundles/core-practices@v1.2.3" → bundles, core-practices, "v1.2.3"
//   - "bundles/core-practices@abc123" → bundles, core-practices, "abc123"
func parseTypePathVersion(s string) (itemType ItemType, itemPath string, contentVersion string, err error) {
	// Drop a legacy schema-version segment (pre-removal "repo@v1/type/path").
	// The v1 directory is gone — git tag/SHA is the sole content version now — so
	// old refs/lockfiles that still carry it resolve instead of erroring.
	s = stripLegacySchemaSegment(s)

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

// stripLegacySchemaSegment removes a leading schema-version segment from a
// pre-removal "schemaVersion/type/path" remainder (e.g. "v1/profiles/x" →
// "profiles/x"). It only strips when the first segment is not itself a type but
// the next one is, so a genuine "type/path" passes through untouched.
func stripLegacySchemaSegment(s string) string {
	first, rest, ok := strings.Cut(s, "/")
	if !ok || isItemTypeDir(first) {
		return s
	}
	if next, _, _ := strings.Cut(rest, "/"); isItemTypeDir(next) {
		return rest
	}
	return s
}

// isItemTypeDir reports whether s names a supported item-type directory.
func isItemTypeDir(s string) bool {
	return s == "bundles" || s == "profiles"
}

// String returns the string representation of a reference.
func (r *Reference) String() string {
	if r.IsLocal {
		return r.localRef()
	}
	return r.CanonicalString()
}

// localRef formats a ctxloom:local reference as
// "ctxloom:local@<type>/<path>[@version]". The version is included when present
// (unlike the canonical URL form, the local form is fully round-trippable).
func (r *Reference) localRef() string {
	typeName := r.ItemType.DirName()
	if typeName == "" {
		typeName = "bundles" // default
	}
	s := fmt.Sprintf("%s@%s/%s", LocalSource, typeName, r.Path)
	if r.ContentVersion != "" {
		s += "@" + r.ContentVersion
	}
	return s
}

// CanonicalString returns the canonical URL representation.
func (r *Reference) CanonicalString() string {
	if r.IsLocal {
		return r.localRef()
	}
	typeName := r.ItemType.DirName()
	if typeName == "" {
		typeName = "bundles" // default
	}
	return fmt.Sprintf("%s@%s/%s", r.URL, typeName, r.Path)
}

// IsCanonical reports whether this is a URL-based reference. A reference is
// canonical exactly when it carries a repository URL; URL-less refs are either
// local (ctxloom:local) or invalid.
func (r *Reference) IsCanonical() bool {
	return r.URL != ""
}

// BuildFilePath constructs the path to the item within the repository.
// For canonical refs, uses the embedded item type.
// For simple refs, uses the provided itemType.
func (r *Reference) BuildFilePath(itemType ItemType) string {
	if r.IsCanonical() {
		// Use the embedded item type for canonical refs.
		itemType = r.ItemType
	} else if r.IsLocal {
		// Read relative to the .ctxloom/local/ root, which is itself inside
		// .ctxloom — so no redundant ctxloom/ segment:
		// .ctxloom/local/bundles/go-tools.yaml.
		return path.Join(r.ItemType.DirName(), r.Path+".yaml")
	}
	// Within a repo: ctxloom/<kind>/<path>.yaml. These are logical,
	// forward-slash repo paths (consumed by go-git / FromSlash on disk), so
	// path.Join is correct here — not filepath.Join.
	return path.Join("ctxloom", itemType.DirName(), r.Path+".yaml")
}

// LocalPath returns the local path where the item would be installed.
// baseDir is the .ctxloom directory path.
// Bundles go in cache/bundles/, profiles go in profiles/ (at root).
func (r *Reference) LocalPath(baseDir string, itemType ItemType) string {
	if r.IsCanonical() {
		itemType = r.ItemType
	}
	// remoteName ("github.com/owner/repo") and r.Path ("lang/go/testing") are
	// logical, forward-slash segments. baseDir is an on-disk OS path, so build
	// with filepath.Join — it cleans the embedded forward slashes to the OS
	// separator, keeping the install path Windows-safe (was fmt.Sprintf("%s/…"),
	// which left forward slashes on Windows).
	remoteName := r.LocalRemoteName()
	file := r.Path + ".yaml"

	switch itemType {
	case ItemTypeProfile:
		// Profiles: .ctxloom/profiles/<remote>/<path>.yaml (root level, no cache layer)
		return filepath.Join(baseDir, paths.ProfilesDir, remoteName, file)
	default:
		// Bundles (and any other type): .ctxloom/cache/bundles/<remote>/<path>.yaml
		return filepath.Join(baseDir, paths.CacheDir, paths.BundlesDir, remoteName, file)
	}
}

// LocalRemoteName returns a filesystem-safe name for the remote.
// For canonical URLs, this extracts a meaningful identifier; for URL-less
// (local) refs it is empty.
func (r *Reference) LocalRemoteName() string {
	if r.URL == "" {
		return ""
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
