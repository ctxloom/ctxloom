package trust

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/refuri"
)

// The URI SYNTAX this grammar is written in — the scheme carrying the source
// class, the "//" repository/bundle split, percent-encoding and dot-segment
// resolution — lives in internal/refuri, below both this package and
// internal/remote. This package adds what refuri deliberately does not know:
// that the "#" fragment names a trust ITEM KIND, and that a parsed reference
// is an identity a grant keys on.
//
// The class names are re-exported rather than re-declared so a caller reading
// trust.ClassGit and a caller reading refuri.ClassGit cannot come to hold two
// different values.
type SourceClass = refuri.SourceClass

const (
	// ClassGit addresses a bundle in a remote git repository reachable by
	// host. It is the only class with an authority.
	ClassGit = refuri.ClassGit
	// ClassFile addresses a bundle in a git repository at an absolute local
	// path. It has no authority: ctxloom+file:///srv/repo//bundles/x.
	ClassFile = refuri.ClassFile
	// ClassBuiltin addresses a bundle embedded in the ctxloom binary.
	ClassBuiltin = refuri.ClassBuiltin
	// ClassLocal addresses a bundle in the project's own tree.
	ClassLocal = refuri.ClassLocal
	// ClassCompanion addresses a bundle from a companion binary's loadout.
	ClassCompanion = refuri.ClassCompanion
)

// Errors returned by the bundle-reference grammar. They are sentinels so a
// caller can tell a syntax error from the one refusal that is policy —
// ErrRefNameCollision (R3) — with errors.Is.
var (
	// ErrRefSyntax indicates the reference does not match the grammar. It IS
	// refuri.ErrSyntax, not a copy: a syntax error raised inside the shared
	// grammar must satisfy errors.Is against the sentinel this package's
	// callers test, or the same malformed reference would be a syntax error at
	// one layer and an unclassified failure at the next.
	ErrRefSyntax = refuri.ErrSyntax

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
	// set for ClassGit and ClassFile. Its case is PRESERVED, on every host —
	// RFC 3986 §6.2.2.1 makes a URI path case-sensitive.
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
// The URI syntax and its canonicalization are refuri.Parse's; read that
// function's doc for the normalization rules and for why canonicalizing at the
// parse boundary is a security property rather than tidiness. What this
// function adds is the fragment's MEANING: refuri carries "#<kind>/<item>" as
// opaque text, and ParseSelector turns it into the trust item kind a grant
// keys on. A reference whose selector names no known kind is refused here even
// though its URI is well formed — an item nobody can name is an item nobody
// can approve.
func ParseBundleRef(raw string) (BundleRef, error) {
	return fromParts(refuri.Parse(raw))
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

// BundleKey is the version-less, item-less canonical identity of a bundle:
// exactly what BundleRef.BundleIdentity() renders. It is a DEFINED type so a
// bare bundle name cannot be passed where a resolution key is expected — the
// two namespaces that U3b-3 merges were, until now, both spelled `string`.
type BundleKey string

// BundleIdentity is Identity with the item selector dropped — the identity of
// the BUNDLE that contains the item, rather than of the item.
func (r BundleRef) BundleIdentity() BundleKey {
	r.Kind = ""
	r.Item = ""
	return BundleKey(r.render(false))
}

// IsItem reports whether the reference addresses an item within the bundle
// rather than the bundle itself.
func (r BundleRef) IsItem() bool { return r.Item != "" }

// parts projects the reference onto the shared URI syntax, rendering the item
// selector back to the opaque fragment refuri carries. It is the single seam
// between the identity this package owns and the syntax refuri owns, so the
// two cannot disagree about what a reference looks like.
func (r BundleRef) parts() refuri.Parts {
	p := refuri.Parts{
		Class:    r.Class,
		Host:     r.Host,
		RepoPath: r.RepoPath,
		Bundle:   r.Bundle,
		Version:  r.Version,
	}
	if r.Item != "" {
		p.Fragment = r.Kind.Dir() + "/" + r.Item
	}
	return p
}

func (r BundleRef) render(withVersion bool) string {
	return r.parts().Render(withVersion)
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

// The bundle-level minters delegate to refuri's, which own the round-trip
// discipline (render, reparse, refuse what a parser would refuse). They exist
// here as well because a caller in this package wants a BundleRef — the shape
// that can carry an item selector — not bare syntax.

// GitRef mints a reference to a bundle in a remote git repository.
func GitRef(host, repoPath, bundle string) (BundleRef, error) {
	return fromParts(refuri.Git(host, repoPath, bundle))
}

// FileRef mints a reference to a bundle in a git repository at an absolute
// local path.
func FileRef(repoPath, bundle string) (BundleRef, error) {
	return fromParts(refuri.File(repoPath, bundle))
}

// BuiltinRef mints a reference to a bundle embedded in the ctxloom binary.
func BuiltinRef(bundle string) (BundleRef, error) {
	return fromParts(refuri.Builtin(bundle))
}

// LocalRef mints a reference to a bundle in the project's own tree.
func LocalRef(bundle string) (BundleRef, error) {
	return fromParts(refuri.Local(bundle))
}

// CompanionRef mints a reference to a companion binary's own loadout.
func CompanionRef(bin string) (BundleRef, error) {
	return fromParts(refuri.Companion(bin))
}

// fromParts lifts minted syntax into a BundleRef, interpreting the fragment as
// this package's item selector. It is ParseBundleRef's body over an already
// -parsed value, so a reference built in Go and one parsed from text are held
// to the same rules AND get the same selector reading.
func fromParts(p refuri.Parts, err error) (BundleRef, error) {
	if err != nil {
		return BundleRef{}, err
	}
	ref := BundleRef{
		Class:    p.Class,
		Host:     p.Host,
		RepoPath: p.RepoPath,
		Bundle:   p.Bundle,
		Version:  p.Version,
	}
	if err := ref.parseSelector(p.Fragment); err != nil {
		return BundleRef{}, err
	}
	return ref, nil
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

// mint applies this package's own rule — an item selector is BOTH halves or
// neither — and then hands the reference to refuri's round-trip discipline, so
// every reference in circulation has passed the same gate regardless of whether
// it arrived as text or was constructed in Go.
func mint(r BundleRef) (BundleRef, error) {
	if (r.Kind == "") != (r.Item == "") {
		return BundleRef{}, fmt.Errorf("%w: item kind and item name must be set together", ErrRefSyntax)
	}
	return fromParts(refuri.Mint(r.parts()))
}
