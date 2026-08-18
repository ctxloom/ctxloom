package trust

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// SourceClass names which of the five reference classes a BundleRef belongs
// to. The class is carried in the URI SCHEME (ctxloom+git, ctxloom+file,
// ctxloom+builtin, ctxloom+local, ctxloom+companion) rather than in a path
// prefix, which is legal per RFC 3986 §3.1 (scheme = ALPHA *( ALPHA / DIGIT /
// "+" / "-" / "." )) and is what leaves the AUTHORITY slot free for the real
// host. That in turn is what makes RFC 3986 §6.2.2.1 host case-folding apply
// natively instead of being re-implemented over a hand-parsed string.
type SourceClass string

const (
	// ClassGit addresses a bundle in a remote git repository reachable by
	// host. It is the only class with an authority.
	ClassGit SourceClass = "git"
	// ClassFile addresses a bundle in a git repository at an absolute local
	// path. It has no authority: ctxloom+file:///srv/repo//bundles/x.
	ClassFile SourceClass = "file"
	// ClassBuiltin addresses a bundle embedded in the ctxloom binary.
	ClassBuiltin SourceClass = "builtin"
	// ClassLocal addresses a bundle in the project's own tree.
	ClassLocal SourceClass = "local"
	// ClassCompanion addresses a bundle from a companion binary's loadout.
	ClassCompanion SourceClass = "companion"
)

// schemePrefix is the shared scheme prefix of every class. The class name is
// appended to it, so scheme and class cannot drift.
const schemePrefix = "ctxloom+"

// bundleMarker is the fixed segment that opens the bundle half of an external
// (git/file) reference, immediately after the "//" separator.
const bundleMarker = "bundles/"

// repoBundleSeparator separates the repository path from the bundle path in an
// external reference, following the go-getter / Terraform convention. It is
// unambiguous because a repository path cannot contain an empty segment, so
// the FIRST occurrence is always the separator. A plain "/" could not work:
// both halves are variable depth (GitLab subgroups on one side, nested bundles
// like "lang/go" on the other), so there is no segment count that splits them.
const repoBundleSeparator = "//"

// Errors returned by the bundle-reference grammar. They are sentinels so a
// caller can tell a syntax error from the two refusals that are policy —
// ErrRefForgeCase (R2) and ErrRefNameCollision (R3) — with errors.Is.
var (
	// ErrRefSyntax indicates the reference does not match the grammar.
	ErrRefSyntax = errors.New("malformed bundle reference")

	// ErrRefForgeCase indicates an uppercase repository path was used against
	// a forge that treats repository paths case-insensitively. See
	// knownCaseFoldForges and ParseBundleRef's R2 handling.
	ErrRefForgeCase = errors.New("repository path must be lowercase on this forge")

	// ErrRefNameCollision indicates two references from one source name
	// bundles or items that differ only by ASCII case. See
	// CheckBundleRefFoldCollisions.
	ErrRefNameCollision = errors.New("bundle or item names differ only by case")
)

// BundleRef is a parsed, canonical ctxloom bundle or item reference.
//
// The grammar is:
//
//	ctxloom+git://<host>[:<port>]/<repo-path>//bundles/<name>[@<ver>][#<kind>/<item>]
//	ctxloom+file://<abs-repo-path>//bundles/<name>[@<ver>][#<kind>/<item>]
//	ctxloom+builtin:<name>[@<ver>][#<kind>/<item>]
//	ctxloom+local:<name>[@<ver>][#<kind>/<item>]
//	ctxloom+companion:<bin>[@<ver>][#<kind>/<item>]
//
// Every field holds the DECODED value; percent-encoding is a property of the
// rendering, not of the identity, and String reapplies it canonically. That is
// only sound because ParseBundleRef refuses an encoded "/" (%2F): it is the one
// escape whose decoding changes the structure of the reference, so a decoded
// field could not tell "a%2Fb" from "a/b" apart again.
//
// Construct one with ParseBundleRef or a minter (GitRef, FileRef, BuiltinRef,
// LocalRef, CompanionRef). The zero value is not meaningful.
type BundleRef struct {
	// Class is the source class, carried in the scheme.
	Class SourceClass

	// Host is the authority, lowercased, possibly carrying ":<port>". It is
	// set only for ClassGit; every other class has no host because there is
	// no host to have.
	Host string

	// RepoPath is the repository path with a leading "/" and no trailing one,
	// set for ClassGit and ClassFile. Its case is PRESERVED except where
	// ParseBundleRef refuses it outright (see ErrRefForgeCase).
	RepoPath string

	// Bundle is the bundle path within the source ("code-quality",
	// "lang/go"), or for the three internal classes the whole opaque name
	// (the bundle name, or the companion binary). Case is preserved
	// byte-exact.
	Bundle string

	// Version is the "@<ver>" suffix. It is NOT part of identity and Identity
	// omits it: grants pin by content hash, not by commit, so a version
	// -agnostic key is deliberate and matches Ref.Key's own omission.
	Version string

	// Kind and Item address an item within the bundle, rendered as the
	// "#<kind>/<item>" fragment. Both are empty when the reference addresses
	// the bundle itself.
	Kind ItemKind
	Item string
}

// ParseBundleRef parses and canonicalizes a bundle reference.
//
// Canonicalization happens HERE, at the parse boundary, and nowhere else:
// every consumer downstream compares canonical strings byte-exact. That
// placement is the whole security property. The canonical string is the
// countersign store address AND part of the signature preimage, so two
// spellings that fail to collapse to one string are not a near miss, they are
// a store MISS — and a missed rejection on a bundle carrying a verified
// publisher signature is not "rejected → pending" but "rejected → ALLOW".
// CanonicalRepoURL's doc states the same property for the URL it canonicalizes.
//
// The rules applied, in order:
//
//   - R1 normalization (RFC 3986 §6.2.2): percent-encoded UNRESERVED
//     characters are decoded, remaining escapes are re-emitted with uppercase
//     hex by String's encoder, scheme and host are lowercased, a trailing
//     slash is dropped and an empty query is dropped. A character that is
//     neither unreserved, sub-delim nor pchar — "|" is the motivating one — is
//     percent-encoded on output, never passed through.
//   - R2 repo-path case: REJECTED on a case-folding forge, PRESERVED
//     everywhere else. See the knownCaseFoldForges branch below.
//   - R3 bundle/item name case: PRESERVED byte-exact here; same-fold
//     collisions across a SET of references are detected by
//     CheckBundleRefFoldCollisions.
//   - R4 dot segments: resolved by resolveDotSegmentsEachSide, which splits on
//     the "//" separator first. Read that function's doc before touching it.
func ParseBundleRef(raw string) (BundleRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return BundleRef{}, fmt.Errorf("%w: empty", ErrRefSyntax)
	}
	if i := strings.IndexFunc(raw, isRefControlRune); i >= 0 {
		// Refused, not stripped. remote.NormalizeRef strips at INGEST, where
		// the input is whatever arrived from argv or a lockfile; by the time a
		// string reaches the grammar it has passed that door, so a control
		// character here is a bug in a caller that skipped ingest, and
		// silently repairing it would hide exactly that. The hazard is
		// concrete: a ref is interpolated verbatim into the LF-delimited
		// countersign preimage, where an embedded LF closes the "ref:" line
		// early and lets the rest of the ref forge the following header lines.
		return BundleRef{}, fmt.Errorf("%w: control character at byte %d", ErrRefSyntax, i)
	}
	if i := indexEncodedSlash(raw); i >= 0 {
		// An encoded slash is refused rather than carried. "/" is the one
		// reserved character with STRUCTURAL meaning in this grammar (it
		// separates path segments, and doubled it separates the repository
		// from the bundle), so "%2F" and "/" are not two spellings of one
		// reference — they are two different references that a decoded field
		// could no longer tell apart. Refusing costs nothing: no segment of a
		// repository path, bundle path or item name can contain a literal "/".
		return BundleRef{}, fmt.Errorf("%w: encoded slash (%%2F) at byte %d is not addressable", ErrRefSyntax, i)
	}

	// R1, RFC 3986 §6.2.2.2, and it MUST happen before url.Parse rather than
	// after. Two reasons, both load-bearing: url.Parse REJECTS a percent
	// -escape in the authority outright ("github%2Ecom" is an error, not a
	// host), so a post-parse decode never gets the chance; and "%2E%2E" must
	// become ".." before §6.2.2.3 dot-segment removal runs, or it survives as
	// an opaque segment here while a downstream consumer that decodes first
	// sees a parent traversal. Decoding only UNRESERVED characters cannot
	// change the parse: none of ALPHA / DIGIT / "-" / "." / "_" / "~" is a URI
	// delimiter.
	decoded, err := decodeUnreservedEscapes(raw)
	if err != nil {
		return BundleRef{}, fmt.Errorf("%w: %v", ErrRefSyntax, err)
	}

	u, err := url.Parse(decoded)
	if err != nil {
		return BundleRef{}, fmt.Errorf("%w: %v", ErrRefSyntax, err)
	}

	// url.Parse has already lowercased the scheme, so the class match is
	// case-insensitive for free (R1, §6.2.2.1).
	class, ok := classForScheme(u.Scheme)
	if !ok {
		return BundleRef{}, fmt.Errorf("%w: unknown scheme %q (want %sgit|%sfile|%sbuiltin|%slocal|%scompanion)",
			ErrRefSyntax, u.Scheme, schemePrefix, schemePrefix, schemePrefix, schemePrefix, schemePrefix)
	}
	if u.User != nil {
		// Credentials address a REQUEST, never a repository. Dropping them
		// silently would make "…//bundles/x" and "alice@…//bundles/x" one
		// identity without saying so; refusing says so.
		return BundleRef{}, fmt.Errorf("%w: userinfo is not part of a bundle reference", ErrRefSyntax)
	}
	if u.RawQuery != "" {
		return BundleRef{}, fmt.Errorf("%w: query string is not part of a bundle reference", ErrRefSyntax)
	}
	// An EMPTY query ("…?") is dropped rather than refused: it carries no user
	// intent to discard. The drop needs no statement here and must not grow
	// one: render builds a FRESH url.URL from the parsed fields, so nothing
	// from u — query, ForceQuery, userinfo, opaque — can reach the canonical
	// string except by being copied across deliberately. That is the invariant
	// R1 rests on; preserve it by extending render's field list, never by
	// rendering u.

	ref := BundleRef{Class: class}
	if err := ref.parseSelector(u.Fragment); err != nil {
		return BundleRef{}, err
	}

	if class == ClassGit || class == ClassFile {
		err = ref.parseExternal(u)
	} else {
		err = ref.parseInternal(u)
	}
	if err != nil {
		return BundleRef{}, err
	}
	return ref, nil
}

// parseExternal fills the repo path and bundle for ClassGit / ClassFile.
func (r *BundleRef) parseExternal(u *url.URL) error {
	if r.Class == ClassGit {
		if u.Host == "" {
			return fmt.Errorf("%w: %sgit requires a host", ErrRefSyntax, schemePrefix)
		}
		// RFC 3986 §6.2.2.1: the host is case-INSENSITIVE, so folding it is
		// the conformant normalization, not a special case like R2 below.
		r.Host = strings.ToLower(u.Host)
		// A "www." host is REFUSED, not folded off. The retired
		// trust.CanonicalRepoURL folded it only on a short list of known
		// forges, which meant "www.git.example.com" and "git.example.com"
		// stayed two identities everywhere else — exactly the silent-collapse
		// hazard this grammar exists to close. Refusing costs nothing real: a
		// forge does not require the "www." subdomain for its API/clone
		// traffic, so the fix is always to drop it, never to keep two
		// spellings alive.
		if strings.HasPrefix(r.Host, "www.") {
			return fmt.Errorf("%w: host %q carries a decorative \"www.\" prefix; write %q",
				ErrRefSyntax, r.Host, strings.TrimPrefix(r.Host, "www."))
		}
	} else if u.Host != "" {
		return fmt.Errorf("%w: %sfile takes no host (use %sfile:///<abs-path>)", ErrRefSyntax, schemePrefix, schemePrefix)
	}

	// EscapedPath, never Path. u.Path is DECODED, and a decoded path can
	// contain a "//" that was written "%2F%2F" — which would be read as the
	// repo/bundle separator and silently produce a different, valid-looking
	// reference. ParseBundleRef refuses %2F before we get here, so the two can
	// no longer disagree, but reading the escaped form keeps this function
	// correct on its own terms rather than by remote assumption.
	repoEsc, bundleEsc, err := resolveDotSegmentsEachSide(u.EscapedPath())
	if err != nil {
		return err
	}

	// The version is split off the ESCAPED bundle path, before decoding: an
	// item written "na%40me" must keep its "@" as data, and decoding first
	// would make it indistinguishable from the version delimiter. String
	// re-encodes "@" in a bundle name for the same reason, which is what makes
	// parse ∘ render idempotent.
	bundlePath := bundleEsc
	if at := strings.LastIndex(bundlePath, "@"); at >= 0 {
		r.Version = bundlePath[at+1:]
		bundlePath = bundlePath[:at]
	}
	if !strings.HasPrefix(bundlePath, bundleMarker) {
		return fmt.Errorf("%w: bundle path must begin %q after %q", ErrRefSyntax, bundleMarker, repoBundleSeparator)
	}
	name := strings.TrimPrefix(bundlePath, bundleMarker)
	if name == "" {
		return fmt.Errorf("%w: empty bundle name", ErrRefSyntax)
	}

	repoPath, err := url.PathUnescape(repoEsc)
	if err != nil {
		return fmt.Errorf("%w: repository path: %v", ErrRefSyntax, err)
	}
	if repoPath == "" || repoPath == "/" {
		return fmt.Errorf("%w: empty repository path", ErrRefSyntax)
	}
	// A ".git" suffix is REFUSED, not stripped. It is a clone-URL cosmetic —
	// the same repository is reachable with or without it — so two spellings
	// that differ only in this suffix must not become two identities. Folding
	// it off (the retired trust.CanonicalRepoURL's approach, http(s) only)
	// works, but a silent fold is one more place a caller can stop trusting
	// what the grammar accepted; refusing tells them once, at the door.
	if strings.HasSuffix(repoPath, ".git") {
		return fmt.Errorf("%w: repository path %q carries a decorative \".git\" suffix; write %q",
			ErrRefSyntax, repoPath, strings.TrimSuffix(repoPath, ".git"))
	}

	// R2. This is a SPECIAL CASE FORCED BY EXTERNAL NON-CONFORMANCE, not a
	// preference and not a general rule. RFC 3986 §6.2.2.1 makes a URI PATH
	// case-SENSITIVE; GitHub, GitLab and Bitbucket nevertheless serve
	// "Foo/Bar" and "foo/bar" as the SAME repository. On those hosts an
	// uppercase repository path is an ERROR that names the lowercase
	// spelling — never a silent rewrite, because rewriting a path is
	// rewriting an identity, and the user would never see which identity
	// their rejection was recorded against. On every other host the case is
	// PRESERVED and accepted: a case-sensitive git server is the CONFORMANT
	// behaviour and must stay addressable.
	//
	// knownCaseFoldForges is therefore read here as a list of hosts where
	// uppercase is an error. It is NOT a rewrite rule and must never be
	// generalized into one.
	if r.Class == ClassGit && knownCaseFoldForges[r.Host] {
		if lower := strings.ToLower(repoPath); lower != repoPath {
			return fmt.Errorf("%w: %s treats repository paths case-insensitively; write %q, not %q",
				ErrRefForgeCase, r.Host, lower, repoPath)
		}
	}

	bundle, err := url.PathUnescape(name)
	if err != nil {
		return fmt.Errorf("%w: bundle name: %v", ErrRefSyntax, err)
	}
	r.RepoPath = repoPath
	r.Bundle = bundle
	return nil
}

// parseInternal fills the bundle for the three opaque classes. They carry no
// authority because there IS no host, and their names are case-SENSITIVE, so
// they take on no normalization duty beyond percent-encoding.
func (r *BundleRef) parseInternal(u *url.URL) error {
	if u.Host != "" || u.Path != "" {
		return fmt.Errorf("%w: %s%s takes an opaque name, not a path", ErrRefSyntax, schemePrefix, r.Class)
	}
	opaque := u.Opaque
	if at := strings.LastIndex(opaque, "@"); at >= 0 {
		r.Version = opaque[at+1:]
		opaque = opaque[:at]
	}
	name, err := url.PathUnescape(opaque)
	if err != nil {
		return fmt.Errorf("%w: name: %v", ErrRefSyntax, err)
	}
	if name == "" {
		return fmt.Errorf("%w: empty %s name", ErrRefSyntax, r.Class)
	}
	r.Bundle = name
	return nil
}

// parseSelector fills Kind and Item from the "#<kind>/<item>" fragment, which
// is the shipped selector shape and matches purl's #subpath (RFC 3986 §3.5).
func (r *BundleRef) parseSelector(frag string) error {
	if frag == "" {
		return nil
	}
	kind, item, err := ParseSelector(frag)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefSyntax, err)
	}
	r.Kind = kind
	r.Item = item
	return nil
}

// String renders the canonical reference, including "@<version>" and the
// "#<kind>/<item>" selector when they are set. It is the exact inverse of
// ParseBundleRef: parsing the result yields an equal BundleRef.
func (r BundleRef) String() string {
	return r.render(true)
}

// Identity renders the canonical reference WITHOUT "@<version>". This is the
// countersign store address and the string that enters the signature preimage.
//
// It replaces the CanonicalURL()+"|"+Key() composition, and replacing it is
// the point (R5). That join was redundant — a canonical URI already carries
// source, bundle and item in one injective string — and its "|" was a framing
// hazard: remote.NormalizeRef strips CONTROL characters only, and "|" is 0x7C,
// so it passed through, letting source "S" with key "a|b" and source "S|a"
// with key "b" both render "S|a|b". Here every component is percent-encoded by
// String's encoder, so no component can spell a delimiter of the string that
// contains it.
//
// The version is omitted deliberately, matching Ref.Key: grants pin by content
// hash, not by commit, so an identity that moved with every commit would key
// every grant to a single revision.
func (r BundleRef) Identity() string {
	return r.render(false)
}

// BundleIdentity is Identity with the item selector dropped — the identity of
// the BUNDLE that contains the item, rather than of the item.
func (r BundleRef) BundleIdentity() string {
	r.Kind = ""
	r.Item = ""
	return r.render(false)
}

// IsItem reports whether the reference addresses an item within the bundle
// rather than the bundle itself.
func (r BundleRef) IsItem() bool { return r.Item != "" }

func (r BundleRef) render(withVersion bool) string {
	u := url.URL{Scheme: schemePrefix + string(r.Class)}
	switch r.Class {
	case ClassGit, ClassFile:
		u.Host = r.Host
		bundle := escapeAt(escapePath(r.Bundle))
		if withVersion && r.Version != "" {
			bundle += "@" + r.Version
		}
		esc := escapePath(r.RepoPath) + repoBundleSeparator + bundleMarker + bundle
		// Setting RawPath alongside Path keeps url.URL's own consistency check
		// satisfied (String uses RawPath only when it unescapes to Path), so
		// the "//" separator and the "%40" survive rendering instead of being
		// re-derived from the decoded path.
		u.RawPath = esc
		if p, err := url.PathUnescape(esc); err == nil {
			u.Path = p
		} else {
			u.Path = esc
		}
	default:
		opaque := escapeAt(escapePath(r.Bundle))
		if withVersion && r.Version != "" {
			opaque += "@" + r.Version
		}
		// url.URL emits Opaque VERBATIM — it applies no encoding of its own —
		// so the escaping above is not belt-and-braces, it is the only
		// escaping an internal-class name gets.
		u.Opaque = opaque
	}
	if r.Item != "" {
		u.Fragment = r.Kind.Dir() + "/" + r.Item
	}
	return u.String()
}

// resolveDotSegmentsEachSide applies RFC 3986 §5.2.4 remove_dot_segments to
// EACH SIDE of the "//" repository/bundle separator independently, and returns
// the two halves so the caller rejoins them with the separator intact.
//
// It is NOT path.Clean, and it must never be replaced by path.Clean. R4 exists
// because path.Clean collapses "//" into "/": handed
// "/acme/repo//bundles/lang/go" it returns "/acme/repo/bundles/lang/go",
// silently merging the repository path into the bundle path and producing a
// DIFFERENT reference that still looks valid. Downstream that is the
// rejected → ALLOW failure mode described on Identity, and it would arrive via
// a one-line tidy-up refactor by someone who saw two path-cleaning
// implementations and deleted the unfamiliar one. This paragraph and
// TestBundleRef_R4_SeparatorSurvivesDotSegments are the defence; the test
// fails if this function is swapped for path.Clean.
//
// Resolving the halves independently is also what stops a ".." in the bundle
// path from climbing OUT of the bundle half and eating the repository path's
// last segment, which a single whole-string resolution would allow.
func resolveDotSegmentsEachSide(escPath string) (repo, bundle string, err error) {
	before, after, found := strings.Cut(escPath, repoBundleSeparator)
	if !found {
		return "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path",
			ErrRefSyntax, repoBundleSeparator)
	}
	return removeDotSegments(before), removeDotSegments(after), nil
}

// removeDotSegments implements RFC 3986 §5.2.4 over a single path, preserving
// whether the path was absolute and whether it ended in a slash — except that
// a trailing slash on a non-root path is DROPPED, per R1's "drop a trailing
// slash". It never sees a "//" separator: resolveDotSegmentsEachSide splits
// those off first.
func removeDotSegments(p string) string {
	absolute := strings.HasPrefix(p, "/")
	out := make([]string, 0, 8)
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		switch seg {
		case "", ".":
			// An empty segment here is safe to drop ONLY because this
			// function never sees the "//" repo/bundle separator —
			// resolveDotSegmentsEachSide splits that off first, so the one
			// other source of an empty segment (a second "//" inside a
			// single half) cannot reach this loop. That invariant rests on
			// bundle paths being FILESYSTEM paths: on that domain "a/b" and
			// "a//b" name the same place, so collapsing the empty segment is
			// a no-op, not a merge. If a bundle path ever became a
			// non-filesystem lookup key, "evil//bundles/x" and
			// "evil/bundles/x" would stop being two spellings of one
			// resource and become a genuine collision between two — the
			// empty-segment collapse would be doing the merging instead of
			// resolveDotSegmentsEachSide's separator split.
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, seg)
		}
	}
	joined := strings.Join(out, "/")
	if absolute {
		return "/" + joined
	}
	return joined
}

// CheckBundleRefFoldCollisions reports an error when two references drawn from
// the SAME source name bundles or items that differ only by ASCII case.
//
// Refusing is the right answer and folding is not, because identity here
// derives from the FILESYSTEM. Filenames are case-sensitive on Linux and
// case-INSENSITIVE on macOS and Windows, so one repository can yield an item
// called "Isolation" on one machine and "isolation" on another — two trust
// keys for one item, on machines that share trust records through git. Folding
// instead would merge genuinely DISTINCT items: "README" and "readme" are both
// legal keys, and collapsing them would let a rejection of one silently apply
// to the other. Refusing costs nothing real, because a repository holding both
// spellings cannot be checked out on macOS at all.
//
// The comparison is scoped to one source — class, host and repository path
// must match byte-exact — precisely because a case-SENSITIVE git server is
// conformant (see R2 on ParseBundleRef): "git.example.com/Foo/repo" and
// "git.example.com/foo/repo" are two different repositories and must not be
// reported as a collision.
func CheckBundleRefFoldCollisions(refs []BundleRef) error {
	seen := make(map[string]BundleRef, len(refs))
	var msgs []string
	for _, r := range refs {
		fold := r.foldKey()
		prior, ok := seen[fold]
		if !ok {
			seen[fold] = r
			continue
		}
		if prior.Identity() == r.Identity() {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("%s vs %s", prior.Identity(), r.Identity()))
	}
	if len(msgs) == 0 {
		return nil
	}
	sort.Strings(msgs)
	return fmt.Errorf("%w: %s", ErrRefNameCollision, strings.Join(msgs, "; "))
}

// foldKey builds the collision bucket: the source identified byte-exact, and
// the bundle/item names case-folded.
func (r BundleRef) foldKey() string {
	return strings.Join([]string{
		string(r.Class),
		r.Host,
		r.RepoPath,
		strings.ToLower(r.Bundle),
		string(r.Kind),
		strings.ToLower(r.Item),
	}, "\x00")
}

// GitRef mints a reference to a bundle in a remote git repository. It renders
// the reference and reparses it, so a minted reference is subject to exactly
// the rules ParseBundleRef enforces — a minter cannot construct a reference a
// parser would refuse.
func GitRef(host, repoPath, bundle string) (BundleRef, error) {
	return mint(BundleRef{Class: ClassGit, Host: host, RepoPath: leadingSlash(repoPath), Bundle: bundle})
}

// FileRef mints a reference to a bundle in a git repository at an absolute
// local path. See GitRef for why it round-trips through the parser.
func FileRef(repoPath, bundle string) (BundleRef, error) {
	return mint(BundleRef{Class: ClassFile, RepoPath: leadingSlash(repoPath), Bundle: bundle})
}

// BuiltinRef mints a reference to a bundle embedded in the ctxloom binary.
func BuiltinRef(bundle string) (BundleRef, error) {
	return mint(BundleRef{Class: ClassBuiltin, Bundle: bundle})
}

// LocalRef mints a reference to a bundle in the project's own tree.
func LocalRef(bundle string) (BundleRef, error) {
	return mint(BundleRef{Class: ClassLocal, Bundle: bundle})
}

// CompanionRef mints a reference to a companion binary's own loadout.
func CompanionRef(bin string) (BundleRef, error) {
	return mint(BundleRef{Class: ClassCompanion, Bundle: bin})
}

// WithItem returns a copy of the reference addressing an item within the
// bundle. It round-trips through the parser for the reason GitRef does.
func (r BundleRef) WithItem(kind ItemKind, item string) (BundleRef, error) {
	r.Kind = kind
	r.Item = item
	return mint(r)
}

// WithVersion returns a copy of the reference carrying "@<version>". The
// version does not enter Identity, so this never changes what the reference
// keys as.
func (r BundleRef) WithVersion(version string) (BundleRef, error) {
	r.Version = version
	return mint(r)
}

// mint renders a hand-built reference and parses the result back, so that
// every reference in circulation has passed the same gate regardless of
// whether it arrived as text or was constructed in Go.
func mint(r BundleRef) (BundleRef, error) {
	if r.Bundle == "" {
		return BundleRef{}, fmt.Errorf("%w: empty bundle name", ErrRefSyntax)
	}
	if (r.Kind == "") != (r.Item == "") {
		return BundleRef{}, fmt.Errorf("%w: item kind and item name must be set together", ErrRefSyntax)
	}
	return ParseBundleRef(r.String())
}

func leadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func classForScheme(scheme string) (SourceClass, bool) {
	name, ok := strings.CutPrefix(scheme, schemePrefix)
	if !ok {
		return "", false
	}
	switch c := SourceClass(name); c {
	case ClassGit, ClassFile, ClassBuiltin, ClassLocal, ClassCompanion:
		return c, true
	default:
		return "", false
	}
}

// escapePath percent-encodes a decoded path using net/url's own path encoder,
// which emits UPPERCASE hex (R1) and leaves "/" structural. Routing every
// component through one encoder is deliberate: an internal-class name and an
// external bundle path must escape identically, or the same name would key
// differently depending on its class.
func escapePath(p string) string {
	u := url.URL{Path: p}
	return u.EscapedPath()
}

// escapeAt encodes "@" so it cannot be mistaken for the version delimiter when
// the rendered reference is parsed again. net/url leaves "@" alone in a path
// (it is a legal pchar), so without this a bundle literally named "na@me"
// would round-trip as bundle "na" at version "me".
func escapeAt(s string) string {
	return strings.ReplaceAll(s, "@", "%40")
}

// isRefControlRune matches the C0 range plus DEL, the characters a ctxloom
// reference can never legally carry. It mirrors remote.isRefControlChar, which
// is unexported there; the duplication is the same deliberate defence-in-depth
// split that signing.CountersignHeader.Validate makes — a layer that shares an
// implementation with the layer it backstops is one layer.
func isRefControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// indexEncodedSlash reports the byte offset of an encoded "/" ("%2F" in any
// hex case), or -1.
func indexEncodedSlash(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && s[i+1] == '2' && (s[i+2] == 'F' || s[i+2] == 'f') {
			return i
		}
	}
	return -1
}

// decodeUnreservedEscapes decodes percent-escapes that encode an UNRESERVED
// character (RFC 3986 §2.3: ALPHA / DIGIT / "-" / "." / "_" / "~") and leaves
// every other escape untouched, uppercasing its hex digits (§6.2.2.1). It is
// safe to run on a whole URI before parsing precisely because no unreserved
// character is a URI delimiter, so decoding one cannot change how the result
// parses.
func decodeUnreservedEscapes(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("truncated percent-escape at byte %d", i)
		}
		hi, lo := unhex(s[i+1]), unhex(s[i+2])
		if hi < 0 || lo < 0 {
			return "", fmt.Errorf("invalid percent-escape %q at byte %d", s[i:i+3], i)
		}
		c := byte(hi<<4 | lo)
		if isUnreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex(s[i+1]))
			b.WriteByte(upperHex(s[i+2]))
		}
		i += 2
	}
	return b.String(), nil
}

func isUnreserved(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	}
	return false
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func upperHex(c byte) byte {
	if 'a' <= c && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}
