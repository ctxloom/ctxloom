package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
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
// Local source (project-authored, committed .ctxloom/content/):
//   - "ctxloom:local@bundles/name"
//   - "ctxloom:local@bundles/name@<rev>" (pinned to a project revision)
//
// Canonical ctxloom URI (the grammar internal/shared/refuri defines; class in the
// scheme, "//" between repository path and bundle path):
//   - "ctxloom+git://github.com/owner/repo//bundles/name[@ver][#kind/item]"
//   - "ctxloom+file:///abs/repo//bundles/name"
//   - "ctxloom+local:name", "ctxloom+companion:bin"
func ParseReference(ref string) (*Reference, error) {
	// Ingest boundary: a reference reaching the grammar carries no control
	// characters (NormalizeRef). Doing it here rather than in each caller is
	// what lets every downstream consumer — canonical strings, lockfile keys,
	// the countersign preimage — treat a parsed Reference's fields as clean.
	ref = NormalizeRef(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty reference")
	}

	// The canonical URI family: class in the scheme, "//" splitting the
	// repository path from the bundle path. Dispatched FIRST because it is the
	// grammar every other spelling here is a predecessor of.
	if refuri.HasScheme(ref) {
		return parseCanonicalURIReference(ref)
	}

	// Local source: ctxloom:local@<type>/<path>[@version]
	if strings.HasPrefix(ref, LocalSource+"@") {
		return parseLocalReference(ref)
	}

	// Companion source: ctxloom:companion@<bin>
	if strings.HasPrefix(ref, CompanionSource+"@") {
		return parseCompanionReference(ref)
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

// ResolveRef resolves a reference that may be written in short same-repo form
// against the source it is read from. It is the one place short ↔ canonical
// expansion happens, used wherever refs are consumed (cascade, sync collection,
// profile resolution).
//
//   - A scheme-qualified canonical ref (https://, git@, file://) or an explicit
//     ctxloom:local ref is already self-contained and is returned unchanged.
//   - Anything else is a short same-repo ref ("demo", "lang/go", "demo@v1") and
//     is expanded against sourceURL: the containing item's source. sourceURL is
//     a git URL for remote content, or LocalSource ("ctxloom:local") when the
//     containing item is read from the project itself.
//
// kind selects the item-type segment of the expanded canonical ref
// (bundles/profiles). A short ref with no source to expand against is an error.
func ResolveRef(ref, sourceURL string, kind ItemType) (*Reference, error) {
	ref, sourceURL = NormalizeRef(ref), NormalizeRef(sourceURL)
	// Already self-contained (canonical URL or ctxloom:local) → as-is.
	if parsed, err := ParseReference(ref); err == nil {
		return parsed, nil
	} else if IsSelfContainedRef(ref) {
		// ref carries its OWN scheme/source token (https://, git@, file://,
		// ctxloom:local@, ctxloom:companion@) — it was never a short same-repo
		// ref to expand in the first place, so a ParseReference failure here is
		// the real, final error. Falling through to short-ref expansion below
		// used to swallow it and re-expand the malformed ref's own text against
		// sourceURL, producing a nonsense-but-valid-looking Reference with no
		// error at all.
		return nil, err
	}

	if ref == "" {
		return nil, fmt.Errorf("empty reference")
	}
	if sourceURL == "" {
		return nil, fmt.Errorf("cannot resolve short reference %q without a source", ref)
	}

	// Short same-repo ref: expand "path[@version]" against the source.
	// LocalSource expands to the ctxloom:local grammar; a URL to the canonical
	// URL grammar — both share the "<source>@<kind>/<path>[@version]" shape.
	canonical := fmt.Sprintf("%s@%s/%s", sourceURL, kind.DirName(), ref)
	parsed, err := ParseReference(canonical)
	if err != nil {
		return nil, fmt.Errorf("invalid short reference %q against %s: %w", ref, sourceURL, err)
	}
	return parsed, nil
}

// ResolveRefString resolves ref against its source and returns the canonical
// ref STRING, preserving a trailing "#item-path" suffix. A self-contained ref
// (canonical URL or ctxloom:local) is returned verbatim. A short same-repo ref
// ("demo", "lang/go") is expanded against sourceURL; when it carries no explicit
// "@version"/hash of its own, it inherits sourceHash — a sibling read from a repo
// at a given commit IS pinned to that commit. On any failure ref is returned
// unchanged (fault tolerant — persist the authored form rather than drop it).
func ResolveRefString(ref, sourceURL, sourceHash string, kind ItemType) string {
	// Normalised at entry, not left to ParseReference: every failure path here
	// returns `ref` VERBATIM (fault tolerant — persist the authored form rather
	// than drop it), so an un-normalised input would be handed straight back.
	ref, sourceURL, sourceHash = NormalizeRef(ref), NormalizeRef(sourceURL), NormalizeRef(sourceHash)
	base, item := SplitItemPath(ref)
	if _, err := ParseReference(base); err == nil {
		return ref // already self-contained
	}
	if sourceURL == "" {
		return ref
	}
	expanded := fmt.Sprintf("%s@%s/%s", sourceURL, kind.DirName(), base)
	// Inherit the container's commit unless the ref already pins its own version.
	if sourceHash != "" && !strings.Contains(base, "@") {
		expanded += "@" + sourceHash
	}
	if _, err := ParseReference(expanded); err != nil {
		return ref
	}
	return expanded + item
}

// parseLocalReference parses ctxloom:local references like:
//   - ctxloom:local@bundles/name (current working copy)
//   - ctxloom:local@bundles/name@<rev> (pinned to a project revision)
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

// parseCompanionReference parses a companion loadout reference:
//   - ctxloom:companion@ltk
//   - ctxloom:companion@ctxloom-companion-foo
//
// Format: ctxloom:companion@<bin>. Deliberately the FLATTEST grammar in this
// file — there is no type/path/version tail, because a companion loadout is
// always exactly one whole bundle (the entirety of what that binary
// contributes), fetched live rather than versioned in a git tree. <bin> is
// validated with the same traversal guard as every other item path
// (validateItemPath) even though it always comes from a PATH lookup in
// practice — defense in depth for anything that builds this ref from
// untrusted input (e.g. a hand-typed `ctxloom trust` ref).
func parseCompanionReference(ref string) (*Reference, error) {
	bin := strings.TrimPrefix(ref, CompanionSource+"@")
	if bin == "" {
		return nil, fmt.Errorf("companion reference %q missing a binary name", ref)
	}
	if err := validateItemPath(bin); err != nil {
		return nil, fmt.Errorf("invalid companion reference %s: %w", ref, err)
	}
	return &Reference{
		URL:         CompanionSource,
		ItemType:    ItemTypeBundle,
		Path:        bin,
		IsCompanion: true,
	}, nil
}

// parseHTTPSReference parses HTTPS URLs like:
//   - https://github.com/owner/repo@bundles/name (latest)
//   - https://github.com/owner/repo@bundles/name@v1.2.3 (pinned tag)
//   - https://github.com/owner/repo@bundles/name@abc123 (pinned SHA)
//
// Format: <repo_url>@<type>/<path>@<content_version>
func parseHTTPSReference(ref string) (*Reference, error) {
	// Split at the @ that introduces the item path, NOT the first @ in the
	// whole string: a URL carrying userinfo
	// (https://user@host/owner/repo@bundles/name) has an earlier @ that is
	// part of the authority, not the item-path separator. The
	// authority section ends at the first "/" after the scheme, so any @
	// before that "/" is userinfo and must be skipped.
	prefixLen := len("https://")
	if strings.HasPrefix(ref, "http://") {
		prefixLen = len("http://")
	}
	slashIdx := strings.IndexByte(ref[prefixLen:], '/')
	if slashIdx == -1 {
		return nil, fmt.Errorf("URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}
	pathStart := prefixLen + slashIdx
	atIdx := strings.IndexByte(ref[pathStart:], '@')
	if atIdx == -1 {
		return nil, fmt.Errorf("URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}
	repoURL := ref[:pathStart+atIdx]
	remainder := ref[pathStart+atIdx+1:] // type/path[@contentVersion]

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
	hostPart, pathPart, found := strings.Cut(afterGit, ":")
	if !found {
		return nil, fmt.Errorf("invalid SSH URL format: %s", ref)
	}

	// Find @ that separates repo from item path
	repoPath, remainder, found := strings.Cut(pathPart, "@") // remainder: type/path[@contentVersion]
	if !found {
		return nil, fmt.Errorf("SSH URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}

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

	// A non-empty host names a REMOTE machine in the file:// URI scheme
	// (RFC 8089) — u.Path below silently dropped it, so
	// "file://host/path@bundles/x" used to resolve to "file:///path" (a
	// DIFFERENT, local repository) instead of erroring. This
	// package's file:// support is local-repository-only; reject rather than
	// silently discard.
	if u.Host != "" {
		return nil, fmt.Errorf("file URL reference %s: a host (%q) is not supported here — use file:///path for a local repository", ref, u.Host)
	}

	// The path will contain repo@type/name[@contentVersion]
	fullPath := u.Path

	// Find @ that separates repo path from item path
	repoPath, remainder, found := strings.Cut(fullPath, "@") // remainder: type/path[@contentVersion]
	if !found {
		return nil, fmt.Errorf("file URL reference missing item path: %s (expected @<type>/<path>)", ref)
	}

	// The repository's fetch location, rendered by the one renderer: a
	// percent-encoded file URL. repoPath is DECODED here, and git decodes a
	// file:// URL again, so concatenating it raw would hand git a different
	// directory the moment the path holds an escape.
	repoURL := refuri.Parts{Class: refuri.ClassFile, RepoPath: repoPath}.FetchURL()

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

	// Strip a fragment/prompt selector (`#fragments/<name>`) — it identifies an
	// item WITHIN the bundle, not the bundle's identity. Keeping it here would
	// bake the selector into the canonical ref / lockfile key, so the fetcher
	// would look for a file literally named "<bundle>#fragments/<name>.yaml".
	// The selector is split off and re-applied at assembly time by the bundle
	// loader (see loader_content.go ParseItemAsk). Done BEFORE the @version
	// split so the "<path>@<version>#sel" form (what ResolveRefString emits)
	// doesn't fold the selector into the version.
	itemPath, selector := SplitItemPath(pathWithVersion)

	// Check for content version suffix: path@contentVersion. The legacy
	// "<path>#sel@<version>" ordering carries its version inside the selector.
	if atIdx := strings.LastIndex(itemPath, "@"); atIdx != -1 {
		contentVersion = itemPath[atIdx+1:]
		itemPath = itemPath[:atIdx]
	} else if atIdx := strings.LastIndex(selector, "@"); atIdx != -1 {
		contentVersion = selector[atIdx+1:]
	}

	if itemPath == "" {
		return "", "", "", fmt.Errorf("empty path")
	}
	// SECURITY: the item path is later joined under a repo root (BuildFilePath)
	// and, for filesystem-backed sources, under a directory root (fsVCS) —
	// reject traversal at parse time so no read path has to re-check.
	if err := validateItemPath(itemPath); err != nil {
		return "", "", "", err
	}

	// Parse item type (only bundles are distributed at the top level; top-level
	// @profiles/ distribution was retired — profiles ship inside bundles).
	switch typeStr {
	case "bundles":
		itemType = ItemTypeBundle
	default:
		return "", "", "", fmt.Errorf("unknown item type: %s (only bundles supported)", typeStr)
	}

	return itemType, itemPath, contentVersion, nil
}

// validateItemPath rejects item paths that could escape their root when joined:
// absolute paths and "."/".." segments (checked across both slash flavors, since
// the path is eventually handed to filepath.Join on the host OS). Git tree
// lookups happen to contain these today, but the filesystem-backed VCS does
// not — so the grammar itself forbids them.
func validateItemPath(p string) error {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return fmt.Errorf("invalid item path %q: absolute paths are not allowed", p)
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." {
			return fmt.Errorf("invalid item path %q: %q path segments are not allowed", p, seg)
		}
	}
	return nil
}

// stripLegacySchemaSegment removes a leading schema-version segment from a
// pre-removal "schemaVersion/type/path" remainder (e.g. "v1/bundles/x" →
// "bundles/x"). It only strips when the first segment is not itself a type but
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
	return s == "bundles"
}

// String returns the string representation of a reference. It is nil-safe: a nil
// receiver renders as "<nil>" rather than panicking, so callers that format a ref
// in an error path (e.g. a FetchItem "cannot handle" guard, which is reached
// precisely for a nil/unhandled ref) never turn that into a crash.
func (r *Reference) String() string {
	if r == nil {
		return "<nil>"
	}
	return r.CanonicalString()
}

// CanonicalString renders this reference as a canonical ctxloom URI
// (ctxloom+git / ctxloom+file / ctxloom+local / ctxloom+companion), carrying
// "@<version>" when the reference pins one: BundleRef().String().
//
// A reference that has no bundle identity is malformed by construction. It is
// rendered for DIAGNOSTICS only, as the address it was given, and says so — it
// is never a key: callers that key use BundleRef or LockKey, which error.
func (r *Reference) CanonicalString() string {
	br, err := r.BundleRef()
	if err != nil {
		diag := r.URL + "@" + ItemTypeBundle.DirName() + "/" + r.Path
		clidiag.Warn("ctxloom", "cannot render %q as a canonical reference (%v)", diag, err)
		return diag
	}
	return br.String()
}

// BundleRef mints this reference's structured bundle reference from its own
// fields — the source class, the repository the URL names (read by the one
// repo-level canonicalizer, refuri.ParseRepoIdentity), the bundle path and the
// content version. It is the ONE place a parsed Reference becomes an identity:
// the lockfile key, the canonical string, a reader's source ref and the key a
// retraction is looked up by all come from here, so no two of them can spell
// one bundle two ways.
func (r *Reference) BundleRef() (trust.BundleRef, error) {
	var (
		br  trust.BundleRef
		err error
	)
	switch {
	case r.IsLocal:
		br, err = trust.LocalRef(r.Path)
	case r.IsCompanion:
		br, err = trust.CompanionRef(r.Path)
	case r.URL == "":
		return trust.BundleRef{}, fmt.Errorf("%w: reference has no source URL", trust.ErrRefSyntax)
	default:
		repo, rerr := refuri.ParseRepoIdentity(r.URL)
		if rerr != nil {
			return trust.BundleRef{}, fmt.Errorf("unparseable repository URL %q: %w", r.URL, rerr)
		}
		switch repo.Class {
		case refuri.ClassGit:
			br, err = trust.GitRef(repo.Host, repo.RepoPath, r.Path)
		case refuri.ClassFile:
			br, err = trust.FileRef(repo.RepoPath, r.Path)
		default:
			return trust.BundleRef{}, fmt.Errorf("%w: source URL %q names no repository", trust.ErrRefSyntax, r.URL)
		}
	}
	if err != nil {
		return trust.BundleRef{}, err
	}
	if r.ContentVersion == "" {
		return br, nil
	}
	return br.WithVersion(r.ContentVersion)
}

// LockKey is the key this reference's lockfile entry is stored under: its
// version-less bundle identity, BundleRef().BundleIdentity(). The lockfile
// keys on identity rather than on the address as typed because the trust gate
// looks a publisher's retraction up by identity, and two spellings of one
// repository must not be two entries — one of which no lookup reaches.
func (r *Reference) LockKey() (trust.BundleKey, error) {
	br, err := r.BundleRef()
	if err != nil {
		return "", err
	}
	return br.BundleIdentity(), nil
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
		// Read relative to the .ctxloom/content/ root, which is itself inside
		// .ctxloom — so no redundant ctxloom/ segment:
		// .ctxloom/content/bundles/go-tools.yaml.
		return ContentItemPath(r.ItemType, r.Path)
	}
	// Within a repo: .ctxloom/content/<kind>/<path>.yaml. These are logical,
	// forward-slash repo paths (consumed by go-git / FromSlash on disk), so
	// path.Join is correct here — not filepath.Join.
	//
	// RepoItemPrefix is the SAME expression PublishPath uses; the two sides
	// naming one value is what stops a fetch looking where no publish wrote.
	return RepoItemPath(itemType, r.Path)
}

// WorktreeDirSuffix marks a cache entry as the git WORKTREE for a pinned
// bundle rather than the bundle directory itself.
//
// It is load-bearing, not decoration: a sparse checkout lays the bundle out at
// its REPOSITORY path inside the worktree, so the worktree root and the bundle
// directory nested in it would otherwise both be named for the bundle. Anything
// searching the cache for a directory bearing the bundle's name would then find
// the root first and read an empty one.
const WorktreeDirSuffix = ".worktree"

// LocalWorktreePath returns the git worktree a pinned DIRECTORY-form bundle is
// checked out into.
//
// One worktree per pinned bundle, off the single clone of its repository, each
// detached at its OWN commit. Per-bundle worktrees rather than one shared
// checkout because two bundles published by one repository can be pinned at
// different commits, and a single checkout cannot represent that.
//
// Git owns what is inside it. There is no second, hand-copied materialization
// to keep in step with the pin, which is what made a moved pin and an
// unmaterialized tree describable as separate states at all.
//
// It sits at <cache>/bundles/<remote>/<path>.<digest>.worktree. remoteName
// and r.Path are logical, forward-slash segments while baseDir is an on-disk
// OS path, so it is built with filepath.Join, which cleans the embedded
// slashes to the OS separator; and it is built from paths.CacheBundlesPath
// rather than from the cache/ and bundles/ parts, so a layout change cannot
// miss it.
//
// The digest is of LockKey, and it is what makes the directory INJECTIVE in the
// bundle's identity. The readable part is not: LocalRemoteName shortens a file
// repository to its last two segments, and a case-folding filesystem merges
// names that differ only in case, though path case is identity. Two lock keys
// sharing one worktree read one tree — whichever was pulled last — so one
// repository's bytes would be served under another's key, a retraction
// included. An unaddressable reference has no lock key and so no directory.
func (r *Reference) LocalWorktreePath(baseDir string) (string, error) {
	key, err := r.LockKey()
	if err != nil {
		return "", fmt.Errorf("no cache directory for %s/%s: %w", r.URL, r.Path, err)
	}
	return filepath.Join(paths.CacheBundlesPath(baseDir), r.LocalRemoteName(), r.Path) +
		"." + identityDigest(key) + WorktreeDirSuffix, nil
}

// identityDigest is a short, filesystem-safe, case-insensitive-safe (lowercase
// hex) digest of a bundle identity. 64 bits keeps a crafted second identity
// that lands in a victim's directory out of reach.
func identityDigest(key trust.BundleKey) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// TreeRepoPath is the repository-relative directory this bundle's tree occupies
// — the path a sparse checkout is narrowed to, and the path the bundle
// therefore lands at inside its worktree.
//
// It resolves through BundleTreeRoots, the same enumeration the fetch probe
// walks, so the directory checked out and the directory read back are one
// expression rather than two that must be kept to agree. BundleTreeRoots is
// ordered newest-format-first and holds exactly one root while a single bundle
// format is live; TestTreeRepoPath_SingleLayoutInvariant pins that, so a format
// overlap that reintroduces a second root goes red here rather than silently
// checking out one root and reading the other.
func (r *Reference) TreeRepoPath() string {
	return BundleTreeRoots(r.BuildFilePath(ItemTypeBundle))[0]
}

// LocalTreePath returns the bundle directory itself: the tree nested inside
// LocalWorktreePath at the bundle's repository path.
//
// This is the directory a reader roots at, and its LAST SEGMENT is the bundle
// id — content.validateBundleID requires a single segment, and a nested
// reference path ("lang/go/testing") is absorbed by the parent rather than
// smuggled into the id.
func (r *Reference) LocalTreePath(baseDir string) (string, error) {
	worktree, err := r.LocalWorktreePath(baseDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(worktree, filepath.FromSlash(r.TreeRepoPath())), nil
}

// LocalRemoteName returns a filesystem-safe name for the remote.
// For canonical URLs, this extracts a meaningful identifier; for URL-less
// (local) refs it is empty.
func (r *Reference) LocalRemoteName() string {
	return containRemoteName(r.localRemoteName())
}

// containRemoteName neutralises path traversal in a computed remote directory
// name. The name is derived from a remote URL, which reaches us from a
// lockfile, and it is then joined onto the cache root by LocalPath — whose
// result callers hand to fs.Remove and friends. None of the derivations below
// strip traversal: httpHostPath's path.Join CLEANS, so "https://x/../.."
// collapses to "..", and sanitizePath only rewrites "://", ":" and "@". A
// ".." segment therefore used to escape .ctxloom/cache/bundles entirely.
//
// Traversal segments are REWRITTEN rather than dropped so two degenerate
// remotes cannot silently collide onto one cache directory.
func containRemoteName(name string) string {
	if name == "" {
		return ""
	}
	segs := strings.Split(filepath.ToSlash(name), "/")
	out := segs[:0]
	for _, seg := range segs {
		switch seg {
		case "", ".":
			// A leading/duplicated separator or a no-op segment: drop it.
		case "..":
			out = append(out, "__")
		default:
			out = append(out, seg)
		}
	}
	return path.Join(out...)
}

func (r *Reference) localRemoteName() string {
	if r.URL == "" {
		return ""
	}

	// Derived from the repository's IDENTITY, not its spelling: a pull
	// installs under the reference as typed and a reader finds the tree again
	// from the lockfile key, so the two must name one directory however the
	// repository was spelled (host case, a trailing slash, scp or https).
	//   https://github.com/owner/repo → github.com/owner/repo
	//   git@github.com:owner/repo     → github.com/owner/repo
	//   file:///path/to/repo          → to/repo
	repo, err := refuri.ParseRepoIdentity(r.URL)
	if err != nil {
		return sanitizePath(r.URL)
	}
	switch repo.Class {
	case refuri.ClassGit:
		return path.Join(repo.Host, repo.RepoPath)
	case refuri.ClassFile:
		parts := strings.Split(strings.Trim(repo.RepoPath, "/"), "/")
		if len(parts) >= 2 {
			return path.Join(parts[len(parts)-2], parts[len(parts)-1])
		}
		return parts[0]
	}
	return sanitizePath(r.URL)
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
