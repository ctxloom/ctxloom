package remote

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/refuri"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// LocalSource is the fixed source token for ctxloom:local references —
// project-authored content under the committed .ctxloom/content/ working copy.
// It mirrors the canonical grammar: LocalSource @ <type>/<path>[@version].
const LocalSource = "ctxloom:local"

// CompanionSource is the fixed source token for ctxloom:companion@<bin>
// references — a bundle emitted live by a companion binary discovered on
// PATH (`<bin> loadout --format json`, signature-envelope spec §4.3/§6
// discovery). This is the FIRST-CLASS, RECOGNIZED source token companion
// loadouts are seeded under: recognized here (so the unrecognized-source
// guard every caller builds on IsSelfContainedRef never fires for it) and mapped to a NON-local,
// NON-builtin trust.Ref (Reference.IsLocal stays false), so companion content
// flows through EffectiveTrust's trusted-signer/approved/pending steps
// exactly like a remote bundle — never auto-allowed, never denied as
// unrecognized.
const CompanionSource = "ctxloom:companion"

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
// Canonical ctxloom URI (the grammar internal/refuri defines; class in the
// scheme, "//" between repository path and bundle path):
//   - "ctxloom+git://github.com/owner/repo//bundles/name[@ver][#kind/item]"
//   - "ctxloom+file:///abs/repo//bundles/name"
//   - "ctxloom+local:name", "ctxloom+companion:bin"
//
// ctxloom+builtin: parses as a URI but has no Reference: a builtin bundle is
// embedded in the binary and has no source to fetch from. See
// parseCanonicalURIReference.
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

// IsSelfContainedRef reports whether ref carries its own scheme/source token
// (a canonical URL or an explicit ctxloom:local/ctxloom:companion ref) rather
// than being a short same-repo reference meant for expansion against a
// container's source. It lets a caller tell "this is scheme-qualified but
// malformed" (a real parse error, fail CLOSED) apart from "this has no scheme
// at all" (a candidate short ref, or a first-party local bundle name).
//
// THIS IS THE ONLY LIST. Two copies of it existed — this one and
// operations.looksLikeSourceRef — and they were not merely duplicated, they
// had DRIFTED: the operations copy recognised any "://" but was missing
// ctxloom:companion@, so a malformed companion ref was downgraded to a
// first-party local bundle name and auto-trusted, i.e. trusted MORE than a
// well-formed one. This function is the union of the two, so neither reach
// is lost:
//
//   - the whole canonical ctxloom+<class>: family, via refuri.HasScheme. Three
//     of the five classes are OPAQUE URIs — "ctxloom+builtin:x" carries no
//     "://" at all — so a "://" test reads them as bare names and grants them
//     the first-party local exemption, which is the fail-OPEN direction for
//     every guard built on this answer.
//   - any "://" ANYWHERE, not just the http/https/file prefixes ParseReference
//     dispatches on. An "ssh://…" or "git://…" ref is scheme-qualified even
//     though ParseReference cannot parse it, and must fail closed rather than
//     be re-read as a bare name.
//   - the git@ scp-like prefix, and both ctxloom: source tokens.
//
// Adding a dispatch prefix to ParseReference means adding it here too;
// anything ParseReference recognises but this does not is a fail-open.
func IsSelfContainedRef(ref string) bool {
	switch {
	case refuri.HasScheme(ref),
		strings.HasPrefix(ref, LocalSource+"@"),
		strings.HasPrefix(ref, CompanionSource+"@"),
		strings.HasPrefix(ref, "git@"),
		strings.Contains(ref, "://"):
		return true
	default:
		return false
	}
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

// companionRef formats a ctxloom:companion reference as
// "ctxloom:companion@<bin>" — the flat grammar parseCompanionReference reads
// back, with no type/path/version tail (see that function's doc).
func (r *Reference) companionRef() string {
	return fmt.Sprintf("%s@%s", CompanionSource, r.Path)
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

// CanonicalString renders this reference as a canonical ctxloom URI
// (ctxloom+git / ctxloom+file / ctxloom+local / ctxloom+companion), carrying
// "@<version>" when the reference pins one. This is the reference's IDENTITY:
// the spelling every API and stored identity outside the lockfile uses, and the
// one parseCanonicalURIReference reads back.
//
// It is NOT the lockfile key. A lockfile entry addresses a FETCH and is keyed
// by LockKey, which spells the same bundle the way the lockfile on disk already
// spells it — see LockKey's own doc for why the two are separate.
//
// There is no ctxloom+builtin arm because no Reference can be builtin: a
// builtin bundle is embedded in the binary, has no source to fetch from, and
// parseCanonicalURIReference refuses the class outright rather than mapping it
// onto ClassLocal.
//
// A reference that cannot be classified into the URI family carries no URL and
// is not local or companion, which makes it malformed by construction. It
// renders as its fetch address rather than as an invented URI, and says so.
func (r *Reference) CanonicalString() string {
	p, err := r.canonicalParts()
	if err != nil {
		clidiag.Warn("ctxloom", "cannot render %q as a canonical reference (%v); using its fetch address", r.LockKey(), err)
		return r.LockKey()
	}
	return p.Render(true)
}

// canonicalParts maps this reference onto the shared URI syntax, minting
// through refuri so a rendered identity is subject to exactly the rules a
// parsed one is.
//
// ClassGit covers every transport that names a repository by host and path.
// The transport is not part of a repository's identity — NormalizeURL already
// folds git@ to https — so ssh://, git:// and https:// converge on one
// identity, exactly as the trust grammar's own conversion does.
func (r *Reference) canonicalParts() (refuri.Parts, error) {
	switch {
	case r.IsLocal:
		p, err := refuri.Local(r.Path)
		if err != nil {
			return refuri.Parts{}, err
		}
		p.Version = r.ContentVersion
		return p, nil
	case r.IsCompanion:
		return refuri.Companion(r.Path)
	case r.URL == "":
		return refuri.Parts{}, fmt.Errorf("reference has no source URL")
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return refuri.Parts{}, fmt.Errorf("source URL %q: %w", r.URL, err)
	}
	var p refuri.Parts
	if u.Scheme == "file" {
		p, err = refuri.File(u.Path, r.Path)
	} else {
		p, err = refuri.Git(u.Host, u.Path, r.Path)
	}
	if err != nil {
		return refuri.Parts{}, err
	}
	p.Version = r.ContentVersion
	return p, nil
}

// LockKey renders this reference as the lockfile/fetch address:
// "<url>@<kind>s/<path>", or the ctxloom:local / ctxloom:companion equivalent.
//
// It is deliberately a DIFFERENT string from CanonicalString. A lockfile entry
// addresses a fetch — which repository, which path — and is keyed on this
// spelling on disk; an identity addresses content and is a canonical URI.
// Rendering one from the other would rewrite every lockfile key the moment the
// identity grammar moved, so the two renderings are separate and each names
// what it is for.
func (r *Reference) LockKey() string {
	if r.IsLocal {
		return r.localRef()
	}
	if r.IsCompanion {
		return r.companionRef()
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
		// Read relative to the .ctxloom/content/ root, which is itself inside
		// .ctxloom — so no redundant ctxloom/ segment:
		// .ctxloom/content/bundles/go-tools.yaml.
		return path.Join(r.ItemType.DirName(), r.Path+".yaml")
	}
	// Within a repo: .ctxloom/content/<kind>/<path>.yaml. These are logical,
	// forward-slash repo paths (consumed by go-git / FromSlash on disk), so
	// path.Join is correct here — not filepath.Join.
	return path.Join(paths.RepoContentPrefix, itemType.DirName(), r.Path+".yaml")
}

// LocalPath returns the local path where the item would be installed.
// baseDir is the .ctxloom directory path. Only bundles are installed at the top
// level (top-level profile distribution was retired): .ctxloom/cache/bundles/.
// This is the CACHE install root for REMOTE-pulled artifacts — project-authored
// bundles live in the committed content tree (paths.LocalBundlesPath).
func (r *Reference) LocalPath(baseDir string, itemType ItemType) string {
	// remoteName ("github.com/owner/repo") and r.Path ("lang/go/testing") are
	// logical, forward-slash segments. baseDir is an on-disk OS path, so build
	// with filepath.Join — it cleans the embedded forward slashes to the OS
	// separator, keeping the install path Windows-safe (was fmt.Sprintf("%s/…"),
	// which left forward slashes on Windows).
	remoteName := r.LocalRemoteName()
	file := r.Path + ".yaml"
	// Built from paths.CacheBundlesPath rather than re-assembling cache/ +
	// bundles/ from their parts, so a layout change in internal/paths cannot
	// silently miss this call site.
	return filepath.Join(paths.CacheBundlesPath(baseDir), remoteName, file)
}

// LocalTreePath returns the local directory a DIRECTORY-form bundle installs
// into: exactly LocalPath minus the ".yaml", so the two shapes of one bundle
// occupy the same name in the cache and cannot both exist under it.
//
// That collision is deliberate. A bundle that changes shape upstream must
// REPLACE its predecessor, not sit beside it — two installs of the same
// reference resolving to two different trees is precisely the ambiguity a
// pinned reference exists to remove.
func (r *Reference) LocalTreePath(baseDir string) string {
	return strings.TrimSuffix(r.LocalPath(baseDir, ItemTypeBundle), ".yaml")
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
