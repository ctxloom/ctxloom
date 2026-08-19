// Package refuri owns the SYNTAX of a ctxloom reference URI — the scheme that
// carries the source class, the authority, the "//" split between repository
// path and bundle path, the "@<ver>" suffix and the "#" fragment — and nothing
// above it.
//
// It sits below both internal/trust (which interprets the fragment as a trust
// item kind and mints BundleRef identities) and internal/remote (which turns a
// reference into a FETCH). Those two packages cannot share the grammar by
// importing each other: trust already imports remote, so the shared syntax has
// to live under both or be written twice. Written twice is the failure this
// package exists to prevent — two parsers are two addressing schemes, and a
// reference accepted by one and refused by the other is a reference whose
// meaning depends on which door it came through.
package refuri

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// SourceClass names which of the five reference classes a URI belongs to. The
// class is carried in the URI SCHEME (ctxloom+git, ctxloom+file,
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

// Classes returns every source class, in a fixed order.
//
// It exists so exhaustiveness over the classes is TESTABLE rather than
// trusted: anything that must answer for every class walks this list, so a
// class added here without being handled there fails that test instead of
// being silently misread at runtime. ClassForScheme is driven off it directly,
// which is what makes this list the definition of the vocabulary rather than a
// second copy of it.
func Classes() []SourceClass {
	return []SourceClass{ClassGit, ClassFile, ClassBuiltin, ClassLocal, ClassCompanion}
}

// SchemePrefix is the shared scheme prefix of every class. The class name is
// appended to it, so scheme and class cannot drift.
const SchemePrefix = "ctxloom+"

// BundleMarker is the fixed segment that opens the bundle half of an external
// (git/file) reference, immediately after the "//" separator.
const BundleMarker = "bundles/"

// RepoBundleSeparator separates the repository path from the bundle path in an
// external reference, following the go-getter / Terraform convention. It is
// unambiguous because a repository path cannot contain an empty segment, so
// the FIRST occurrence is always the separator. A plain "/" could not work:
// both halves are variable depth (GitLab subgroups on one side, nested bundles
// like "lang/go" on the other), so there is no segment count that splits them.
const RepoBundleSeparator = "//"

// ErrSyntax indicates the reference does not match the grammar. It is a
// sentinel so a caller can tell a syntax error from a refusal that is policy.
var ErrSyntax = errors.New("malformed bundle reference")

// Parts is one parsed ctxloom reference URI. Every field holds the DECODED
// value; percent-encoding is a property of the rendering, not of the identity,
// and Render reapplies it canonically. That is only sound because Parse
// refuses an encoded "/" (%2F): it is the one escape whose decoding changes
// the structure of the reference, so a decoded field could not tell "a%2Fb"
// from "a/b" apart again.
//
// Fragment is carried as raw text, uninterpreted. What a fragment MEANS is the
// business of the layer above — this package guarantees only that it survives
// parse and render byte-exact.
type Parts struct {
	// Class is the source class, carried in the scheme.
	Class SourceClass

	// Host is the authority, lowercased, possibly carrying ":<port>". It is
	// set only for ClassGit; every other class has no host because there is
	// no host to have.
	Host string

	// RepoPath is the repository path with a leading "/" and no trailing one,
	// set for ClassGit and ClassFile. Its case is PRESERVED, on every host —
	// RFC 3986 §6.2.2.1 makes a URI path case-sensitive.
	RepoPath string

	// Bundle is the bundle path within the source ("code-quality",
	// "lang/go"), or for the three internal classes the whole opaque name
	// (the bundle name, or the companion binary). Case is preserved
	// byte-exact.
	Bundle string

	// Version is the "@<ver>" suffix. It is NOT part of identity: a caller
	// rendering an identity passes withVersion=false to Render.
	Version string

	// Fragment is the "#" fragment with the "#" removed, decoded and
	// otherwise uninterpreted.
	Fragment string
}

// IsExternal reports whether the class addresses a repository — the two
// classes that carry a repository path, and the only two whose URI has a path
// half at all.
func (p Parts) IsExternal() bool {
	return p.Class == ClassGit || p.Class == ClassFile
}

// HasScheme reports whether raw CLAIMS the ctxloom URI family, by opening with
// the shared scheme prefix. It says nothing about whether the class that
// follows is one this package knows, and that width is deliberate.
//
// A caller uses it to tell "scheme-qualified" (fail CLOSED — refuse, and say
// why) apart from "no scheme at all" (a bare name, first-party by
// construction, and the only input that takes the local exemption). Narrowing
// this to the KNOWN classes would put "ctxloom+registry:x" — a reference
// naming a class this build does not implement — on the bare-name side, where
// it would be granted the exemption instead of being refused. A reference from
// a newer grammar must fail, not be adopted.
//
// Parse is where a class is checked, and it refuses every scheme this
// recogniser admits and Classes() does not name.
func HasScheme(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), SchemePrefix)
}

// Parse parses and canonicalizes a reference URI.
//
// Canonicalization happens HERE, at the parse boundary, and nowhere else:
// every consumer downstream compares canonical strings byte-exact. That
// placement is the whole security property. The canonical string is the
// countersign store address AND part of the signature preimage, so two
// spellings that fail to collapse to one string are not a near miss, they are
// a store MISS — and a missed rejection on a bundle carrying a verified
// publisher signature is not "rejected → pending" but "rejected → ALLOW".
//
// Normalization here is RFC 3986 §6.2 and NOTHING MORE. ".git" suffixes,
// "www." prefixes and repository-path case are PRESERVED byte-exact, and two
// spellings of one repository are two identities. That is deliberate: whether
// two addresses reach the same repository is host-specific knowledge we do not
// have, and folding on a guess merges two identities onto one trust key —
// which would let a rejection of one silently govern the other. The inverse,
// refusing a non-preferred spelling, is worse still: "https://host/foo.git" IS
// the real path of a bare repository on a plain git server, where
// "https://host/foo" often does not exist, so refusing it makes a real
// repository UNADDRESSABLE.
//
// The cross-address case is already handled one layer over: a CONTENT-reject
// omits the ref by design, so a rejection of these bytes holds wherever they
// appear. A REF-reject blocks one ADDRESS, which is what it says. Refusal here
// is reserved for spellings that are MALFORMED — "%2F" forging a separator, a
// control character, a missing "bundles/" prefix — never for spellings that
// are merely non-preferred.
//
// The rules applied, in order:
//
//   - R1 normalization (RFC 3986 §6.2.2): percent-encoded UNRESERVED
//     characters are decoded, remaining escapes are re-emitted with uppercase
//     hex by Render's encoder, scheme and host are lowercased, a trailing
//     slash is dropped and an empty query is dropped. A character that is
//     neither unreserved, sub-delim nor pchar — "|" is the motivating one — is
//     percent-encoded on output, never passed through.
//   - R2 repo-path case: PRESERVED byte-exact, on every host. RFC 3986
//     §6.2.2.1 makes a URI path case-SENSITIVE, and this grammar does not
//     second-guess it.
//   - R3 bundle/item name case: PRESERVED byte-exact here; same-fold
//     collisions across a SET of references are detected one layer up.
//   - R4 dot segments: resolved by resolveDotSegmentsEachSide, which splits on
//     the "//" separator first. Read that function's doc before touching it.
func Parse(raw string) (Parts, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Parts{}, fmt.Errorf("%w: empty", ErrSyntax)
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
		return Parts{}, fmt.Errorf("%w: control character at byte %d", ErrSyntax, i)
	}
	if i := indexEncodedSlash(raw); i >= 0 {
		// An encoded slash is refused rather than carried. "/" is the one
		// reserved character with STRUCTURAL meaning in this grammar (it
		// separates path segments, and doubled it separates the repository
		// from the bundle), so "%2F" and "/" are not two spellings of one
		// reference — they are two different references that a decoded field
		// could no longer tell apart. Refusing costs nothing: no segment of a
		// repository path, bundle path or item name can contain a literal "/".
		return Parts{}, fmt.Errorf("%w: encoded slash (%%2F) at byte %d is not addressable", ErrSyntax, i)
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
		return Parts{}, fmt.Errorf("%w: %v", ErrSyntax, err)
	}

	u, err := url.Parse(decoded)
	if err != nil {
		return Parts{}, fmt.Errorf("%w: %v", ErrSyntax, err)
	}

	// url.Parse has already lowercased the scheme, so the class match is
	// case-insensitive for free (R1, §6.2.2.1).
	class, ok := ClassForScheme(u.Scheme)
	if !ok {
		return Parts{}, fmt.Errorf("%w: unknown scheme %q (want %sgit|%sfile|%sbuiltin|%slocal|%scompanion)",
			ErrSyntax, u.Scheme, SchemePrefix, SchemePrefix, SchemePrefix, SchemePrefix, SchemePrefix)
	}
	if u.User != nil {
		// Credentials address a REQUEST, never a repository. Dropping them
		// silently would make "…//bundles/x" and "alice@…//bundles/x" one
		// identity without saying so; refusing says so.
		return Parts{}, fmt.Errorf("%w: userinfo is not part of a bundle reference", ErrSyntax)
	}
	if u.RawQuery != "" {
		return Parts{}, fmt.Errorf("%w: query string is not part of a bundle reference", ErrSyntax)
	}
	// An EMPTY query ("…?") is dropped rather than refused: it carries no user
	// intent to discard. The drop needs no statement here and must not grow
	// one: Render builds a FRESH url.URL from the parsed fields, so nothing
	// from u — query, ForceQuery, userinfo, opaque — can reach the canonical
	// string except by being copied across deliberately. That is the invariant
	// R1 rests on; preserve it by extending Render's field list, never by
	// rendering u.

	p := Parts{Class: class, Fragment: u.Fragment}
	if p.IsExternal() {
		err = p.parseExternal(u)
	} else {
		err = p.parseInternal(u)
	}
	if err != nil {
		return Parts{}, err
	}
	return p, nil
}

// parseExternal fills the repo path and bundle for ClassGit / ClassFile.
func (p *Parts) parseExternal(u *url.URL) error {
	if p.Class == ClassGit {
		if u.Host == "" {
			return fmt.Errorf("%w: %sgit requires a host", ErrSyntax, SchemePrefix)
		}
		// RFC 3986 §6.2.2.1: the host is case-INSENSITIVE, so folding its
		// case is conformant normalization. A "www." prefix is NOT: it is a
		// distinct host name, preserved byte-exact like every other spelling
		// this grammar accepts. See Parse's doc.
		p.Host = strings.ToLower(u.Host)
	} else if u.Host != "" {
		return fmt.Errorf("%w: %sfile takes no host (use %sfile:///<abs-path>)", ErrSyntax, SchemePrefix, SchemePrefix)
	}

	// EscapedPath, never Path. u.Path is DECODED, and a decoded path can
	// contain a "//" that was written "%2F%2F" — which would be read as the
	// repo/bundle separator and silently produce a different, valid-looking
	// reference. Parse refuses %2F before we get here, so the two can no
	// longer disagree, but reading the escaped form keeps this function
	// correct on its own terms rather than by remote assumption.
	repoEsc, bundleEsc, err := resolveDotSegmentsEachSide(u.EscapedPath())
	if err != nil {
		return err
	}

	// The version is split off the ESCAPED bundle path, before decoding: an
	// item written "na%40me" must keep its "@" as data, and decoding first
	// would make it indistinguishable from the version delimiter. Render
	// re-encodes "@" in a bundle name for the same reason, which is what makes
	// parse ∘ render idempotent.
	bundlePath := bundleEsc
	if at := strings.LastIndex(bundlePath, "@"); at >= 0 {
		p.Version = bundlePath[at+1:]
		bundlePath = bundlePath[:at]
	}
	if !strings.HasPrefix(bundlePath, BundleMarker) {
		return fmt.Errorf("%w: bundle path must begin %q after %q", ErrSyntax, BundleMarker, RepoBundleSeparator)
	}
	name := strings.TrimPrefix(bundlePath, BundleMarker)
	if name == "" {
		return fmt.Errorf("%w: empty bundle name", ErrSyntax)
	}

	repoPath, err := url.PathUnescape(repoEsc)
	if err != nil {
		return fmt.Errorf("%w: repository path: %v", ErrSyntax, err)
	}
	if repoPath == "" || repoPath == "/" {
		return fmt.Errorf("%w: empty repository path", ErrSyntax)
	}
	bundle, err := url.PathUnescape(name)
	if err != nil {
		return fmt.Errorf("%w: bundle name: %v", ErrSyntax, err)
	}
	p.RepoPath = repoPath
	p.Bundle = bundle
	return nil
}

// parseInternal fills the bundle for the three opaque classes. They carry no
// authority because there IS no host, and their names are case-SENSITIVE, so
// they take on no normalization duty beyond percent-encoding.
func (p *Parts) parseInternal(u *url.URL) error {
	if u.Host != "" || u.Path != "" {
		return fmt.Errorf("%w: %s%s takes an opaque name, not a path", ErrSyntax, SchemePrefix, p.Class)
	}
	opaque := u.Opaque
	if at := strings.LastIndex(opaque, "@"); at >= 0 {
		p.Version = opaque[at+1:]
		opaque = opaque[:at]
	}
	name, err := url.PathUnescape(opaque)
	if err != nil {
		return fmt.Errorf("%w: name: %v", ErrSyntax, err)
	}
	if name == "" {
		return fmt.Errorf("%w: empty %s name", ErrSyntax, p.Class)
	}
	p.Bundle = name
	return nil
}

// Render renders the canonical reference URI, including the "#" fragment when
// set and "@<version>" when withVersion. It is the exact inverse of Parse:
// parsing the result yields equal Parts.
//
// withVersion=false is what an IDENTITY renders as. The version is omitted
// from an identity deliberately: grants pin by content hash, not by commit, so
// an identity that moved with every commit would key every grant to a single
// revision.
func (p Parts) Render(withVersion bool) string {
	u := url.URL{Scheme: SchemePrefix + string(p.Class)}
	if p.IsExternal() {
		u.Host = p.Host
		bundle := escapeAt(escapePath(p.Bundle))
		if withVersion && p.Version != "" {
			bundle += "@" + p.Version
		}
		esc := escapePath(p.RepoPath) + RepoBundleSeparator + BundleMarker + bundle
		// Setting RawPath alongside Path keeps url.URL's own consistency check
		// satisfied (String uses RawPath only when it unescapes to Path), so
		// the "//" separator and the "%40" survive rendering instead of being
		// re-derived from the decoded path.
		u.RawPath = esc
		if unescaped, err := url.PathUnescape(esc); err == nil {
			u.Path = unescaped
		} else {
			u.Path = esc
		}
	} else {
		opaque := escapeAt(escapePath(p.Bundle))
		if withVersion && p.Version != "" {
			opaque += "@" + p.Version
		}
		// url.URL emits Opaque VERBATIM — it applies no encoding of its own —
		// so the escaping above is not belt-and-braces, it is the only
		// escaping an internal-class name gets.
		u.Opaque = opaque
	}
	u.Fragment = p.Fragment
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
// rejected → ALLOW failure mode described on Parse, and it would arrive via
// a one-line tidy-up refactor by someone who saw two path-cleaning
// implementations and deleted the unfamiliar one. This paragraph and
// TestBundleRef_R4_SeparatorSurvivesDotSegments are the defence; the test
// fails if this function is swapped for path.Clean.
//
// Resolving the halves independently is also what stops a ".." in the bundle
// path from climbing OUT of the bundle half and eating the repository path's
// last segment, which a single whole-string resolution would allow.
func resolveDotSegmentsEachSide(escPath string) (repo, bundle string, err error) {
	before, after, found := strings.Cut(escPath, RepoBundleSeparator)
	if !found {
		return "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path",
			ErrSyntax, RepoBundleSeparator)
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

// LeadingSlash returns p with a leading "/", which is the shape RepoPath is
// stored in so two spellings of one repository path cannot key differently.
func LeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// ClassForScheme maps a URI scheme to its source class. ok is false for any
// scheme outside the ctxloom+ family.
func ClassForScheme(scheme string) (SourceClass, bool) {
	name, ok := strings.CutPrefix(scheme, SchemePrefix)
	if !ok {
		return "", false
	}
	for _, c := range Classes() {
		if SourceClass(name) == c {
			return c, true
		}
	}
	return "", false
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
// is unexported there; the duplication is a deliberate defence-in-depth split,
// the same one signing.CountersignHeader.Validate makes — a layer that shares
// an implementation with the layer it backstops is one layer.
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

// isUnreserved reports whether c is RFC 3986 §2.3 unreserved — the exact set
// whose percent-escape may be decoded without changing what a URI means.
//
// reprise:ignore — it shares the ASCII-alphanumeric idiom with
// ltk/engine.isUnsafePathRune (a shell-neutral argv set) and
// tasks/paths.projectIDRune (a path-safety set), but the three sets are not
// the same set and are owned by three different specifications. Folding them
// into one helper would let a change made for one domain silently redefine
// what the other two consider safe — which is the failure the separation
// exists to prevent, not an accident of copying.
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
