package remote

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/refuri"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A ctxloom reference CANNOT carry a control character. The grammar is built
// entirely from URL text, "/"-separated path segments and "#<kind>/<name>"
// selectors, none of which admit one — so a control character in a ref is
// either a bug upstream or an attack, never authored intent.
//
// It is a security property, not tidiness. A ref is interpolated verbatim into
// the LF-delimited countersign preimage (signing.CountersignPayload), where an
// embedded LF closes the `ref:` line early and lets the remainder of the ref
// forge the `form:` and `len:` lines the framing emits after it — two distinct
// (assertion, ref, form, payload) tuples framing to identical bytes, so one
// signature verifies for both and both file at one index hash. It is also
// rendered to the human whose approval is the entire point of the review gate:
// CR, backspace and ESC let a hostile ref repaint the terminal so the string
// shown is not the string being approved.
//
// The whole C0 range plus DEL is stripped rather than only CR/LF, because both
// hazards above generalise past the two characters that happen to break the
// frame, and no legal ref loses anything.
//
// The rule is enforced twice, deliberately and independently: stripped HERE, at
// ingest, so no consumer has to re-check; and REFUSED in
// signing.CountersignHeader.Validate, because the frame is the thing being
// signed and must not depend on any caller having come through this door. The
// second layer does not import this one — a defence in depth that shares an
// implementation is one layer.
//
// Deleting is the INGEST answer and is confined to it. Nothing here is
// exported for a display path to borrow: a string on its way to a terminal is
// shared/termsafe's business, and termsafe ESCAPES rather than deletes so the
// human reading a trust line can see that a publisher put a control byte
// there. Deletion on a display surface is lossy AND silent — two refs
// differing only by a control character render identically — which is exactly
// the forgery the trust surface exists to prevent.
func isRefControlChar(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// NormalizeRef is the ingest normaliser every reference passes through as it
// enters ctxloom from argv, a config or bundle file, a lockfile, a remote
// payload or an MCP argument. It strips control characters (see
// isRefControlChar) and returns the cleaned ref.
//
// A strip is NEVER silent: normalising user input without saying so is how a
// malformed ref becomes an invisible one. The warning is additive — it does not
// change control flow, and a ref that needed no cleaning is returned byte for
// byte. Both refs are printed with %q, which escapes exactly the characters
// being complained about, so the diagnostic itself cannot be used to paint the
// terminal.
func NormalizeRef(ref string) string {
	if strings.IndexFunc(ref, isRefControlChar) == -1 {
		return ref
	}
	clean := strings.Map(func(r rune) rune {
		if isRefControlChar(r) {
			return -1
		}
		return r
	}, ref)
	clidiag.WarnOnce("ctxloom", "reference %q contained control characters and was read as %q "+
		"— a ctxloom reference cannot carry them; this is a bug upstream or an attempt to forge one", ref, clean)
	return clean
}

// FragmentSelector is the selector prefix addressing a fragment within a
// bundle ("<bundle>#fragments/<name>"). Producers (bundle expansion) and
// consumers (reference parsing, exclusion matching) share this constant so
// the selector grammar lives in one place.
const FragmentSelector = "#fragments/"

// FragmentName returns the bare fragment name from a ref carrying a
// "#fragments/" selector; ok is false when ref has none.
func FragmentName(ref string) (name string, ok bool) {
	ref = NormalizeRef(ref)
	if i := strings.Index(ref, FragmentSelector); i != -1 {
		return ref[i+len(FragmentSelector):], true
	}
	return "", false
}

// SplitItemPath separates a bundle reference's URL/name part from an optional
// "#item-path" suffix (e.g. "...#fragments/name"). When no suffix is present,
// itemPath is empty and base is the input unchanged. Exported so callers
// outside this package (e.g. operations dependency-URL collection) share the
// one splitter instead of copying it.
func SplitItemPath(ref string) (base, itemPath string) {
	ref = NormalizeRef(ref)
	if hashIdx := strings.Index(ref, "#"); hashIdx != -1 {
		return ref[:hashIdx], ref[hashIdx:]
	}
	return ref, ""
}

// CanonicalKey parses ref and returns its version-less canonical IDENTITY —
// the canonical ctxloom URI ("ctxloom+git://<host>/<repo>//bundles/<path>", or
// the ctxloom+local / ctxloom+companion / ctxloom+file equivalent). ok is false
// when ref does not parse as a reference at all (e.g. a plain local bundle
// name).
//
// This is NOT the lockfile key: a lockfile entry addresses a fetch and is keyed
// on Reference.LockKey. Both spellings parse back to the same Reference, which
// is what lets an identity move without moving a fetch address.
func CanonicalKey(ref string) (string, bool) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return "", false
	}
	parsed.ContentVersion = ""
	return parsed.CanonicalString(), true
}

// LocalBundleRef returns the canonical identity of a plain local bundle name
// ("dev" → "ctxloom+local:dev"). It renders through refuri so a bundle name
// carrying a character the URI grammar escapes cannot key two ways depending on
// which side built the string.
func LocalBundleRef(name string) string {
	p, err := refuri.Local(NormalizeRef(name))
	if err != nil {
		// A name the grammar refuses has no canonical identity. Returning it
		// verbatim keeps the caller's fault-tolerant "resolve to the ask" path
		// intact — the load step then reports a bundle it cannot find, which is
		// what an unaddressable name IS.
		return NormalizeRef(name)
	}
	return p.Render(false)
}

// CanonicalBundleRef returns the canonical pipeline identity for a bundle
// reference: its version-less canonical URI when it parses as a reference
// (a canonical URI, a remote URL or ctxloom:local), else the ctxloom+local
// URI for a plain local bundle name. Every fragment name the assembly
// pipeline carries is "<CanonicalBundleRef>#fragments/<name>", so identities
// compare exactly and local/remote bundles with colliding names stay
// distinguishable.
//
// It ERRORS on a ref that carries a scheme or source token and does not parse,
// and that error is the point of the function's signature. The local fallback
// may only be reached by a BARE NAME. A ref that names a source and fails to
// parse is not a local bundle called "https://…" — feeding it to
// LocalBundleRef mints "ctxloom:local@bundles/<the entire ref>", a
// syntactically valid identity for content that does not exist, which every
// consumer downstream then reports as a MISSING bundle rather than as the
// grammar fault it is. IsSelfContainedRef draws that line, which is why the
// two must know the same set of scheme markers.
func CanonicalBundleRef(name string) (string, error) {
	name = NormalizeRef(name)
	if ck, ok := CanonicalKey(name); ok {
		return ck, nil
	}
	if IsSelfContainedRef(name) {
		return "", fmt.Errorf("bundle reference %q names a source but does not parse: "+
			"it is not a local bundle name and cannot be read as one", name)
	}
	return LocalBundleRef(name), nil
}

// CanonicalFragmentRef canonicalizes the bundle part of a qualified fragment
// ref ("X#fragments/n" → "<CanonicalBundleRef(X)>#fragments/n"). Refs without
// a fragment selector (bare names) are returned unchanged. Any "@<commit>"
// content version is dropped — the canonical fragment identity is
// version-agnostic (see SplitFragmentVersion to keep the version).
func CanonicalFragmentRef(ref string) (string, error) {
	canonical, _, err := SplitFragmentVersion(ref)
	return canonical, err
}

// SplitFragmentVersion canonicalizes the bundle part of a qualified fragment
// ref and splits off any "@<commit>" content version it carries:
// "X@<commit>#fragments/n" → ("<CanonicalBundleRef(X)>#fragments/n", "<commit>").
// An unversioned qualified ref yields an empty version; a ref without a fragment
// selector (a bare name) is returned unchanged with no version. The returned
// Name is the version-AGNOSTIC canonical identity (so dedup/exclusion/ordering
// stay version-agnostic); the version is meant to be honored only at the
// read/resolution path.
func SplitFragmentVersion(ref string) (canonical, version string, err error) {
	ref = NormalizeRef(ref)
	base, sel := SplitItemPath(ref)
	if !strings.HasPrefix(sel, FragmentSelector) {
		return ref, "", nil
	}
	if parsed, perr := ParseReference(base); perr == nil {
		version = parsed.ContentVersion
	}
	bundle, err := CanonicalBundleRef(base)
	if err != nil {
		return "", "", err
	}
	return bundle + sel, version, nil
}

// CommandSelector is the selector prefix addressing a command within a bundle
// ("<bundle>#commands/<name>"). The command counterpart to FragmentSelector;
// producers and consumers share it so the grammar lives in one place. (The
// bundle item-kind was renamed prompt→skill→command; the selector is
// "#commands/".)
const CommandSelector = "#commands/"

// SplitPromptVersion is the command counterpart to SplitFragmentVersion: it
// canonicalizes the bundle part of a qualified command ref and splits off any
// "@<commit>" content version it carries, whether the version sits on the
// bundle part ("X@<commit>#commands/n") or trails the command name
// ("X#commands/n@<commit>", the name-addressed CLI/resource form). The returned
// canonical is the version-AGNOSTIC identity ("<CanonicalBundleRef(X)>#commands/n"),
// so the trust gate and dedup stay version-agnostic; the version is meant to be
// honored only at the read/resolution path (GetPromptAtVersion). A ref without a
// command selector (a bare name) is returned unchanged with no version — a bare
// name has no bundle to pin a historical version against.
func SplitPromptVersion(ref string) (canonical, version string, err error) {
	ref = NormalizeRef(ref)
	base, sel := SplitItemPath(ref)
	if !strings.HasPrefix(sel, CommandSelector) {
		return ref, "", nil
	}
	// Version on the bundle part: "X@<commit>#commands/n".
	if parsed, perr := ParseReference(base); perr == nil {
		version = parsed.ContentVersion
	}
	// Version trailing the command name: "X#commands/n@<commit>" (wins if present).
	if atIdx := strings.LastIndex(sel, "@"); atIdx != -1 {
		version = sel[atIdx+1:]
		sel = sel[:atIdx]
	}
	bundle, err := CanonicalBundleRef(base)
	if err != nil {
		return "", "", err
	}
	return bundle + sel, version, nil
}

// ProfileSelector is the selector prefix addressing a profile shipped INSIDE a
// bundle ("<bundle>#profiles/<name>"). Profiles are an ungated, COMPOUND bundle
// item kind — a profile composes leaves (fragments/commands/mcp/hooks/llm/parents/
// variables) — so the selector is the profile counterpart to FragmentSelector /
// CommandSelector, keeping the bundle-item grammar in one place. Unlike those,
// there is no trust kind for profiles: a profile definition is orchestration/
// config, carrying no review state and never gated. Its constituent leaves still gate at
// their own chokes (fragments/commands at content assembly, mcp/hooks at the exec
// choke) — only the profile definition itself is ungated.
const ProfileSelector = "#profiles/"

// BundleProfileRef builds the canonical reference to a profile shipped in a
// bundle: "<CanonicalBundleRef(bundle)>#profiles/<name>". This is the identity
// the profile loader resolves a bundle-sourced profile by (see the config
// bundle-profile seed), mirroring the "<bundle>#fragments/<name>" fragment
// identity so a bundle profile and a top-level/local profile resolve through the
// same loader.
func BundleProfileRef(bundle, name string) (string, error) {
	canonical, err := CanonicalBundleRef(bundle)
	if err != nil {
		return "", err
	}
	return canonical + ProfileSelector + NormalizeRef(name), nil
}

// CanonicalProfileKey returns the version-less canonical identity of a
// bundle-profile ref — "<CanonicalBundleRef(bundle)>#profiles/<name>" — the
// key shape the config bundle-profile seed uses. Any "@<version>" pin is
// dropped whether it sits on the bundle part ("X@<sha>#profiles/n") or trails
// the profile name ("X#profiles/n@<sha>"): the lockfile pins the bundle, so
// profile identity stays version-agnostic (the profile mirror of
// SplitFragmentVersion/SplitPromptVersion). ok is false when ref carries no
// "#profiles/" selector, and also when its bundle part names a source that
// does not parse. NOTE CanonicalKey is NOT a substitute: it parses the
// whole ref and drops the item selector entirely, collapsing a bundle profile
// to its bundle.
func CanonicalProfileKey(ref string) (string, bool) {
	bundle, name, ok := SplitBundleProfileRef(ref)
	if !ok {
		return "", false
	}
	if atIdx := strings.LastIndex(name, "@"); atIdx != -1 {
		name = name[:atIdx]
	}
	key, err := BundleProfileRef(bundle, name)
	if err != nil {
		// ok=false, never a key built on an unparsed bundle part. Reporting
		// success on a ref this function could not canonicalize is what turns
		// a grammar fault into a silent seed MISS: the caller looks the
		// mangled key up, finds nothing, and reports the profile as not
		// installed.
		return "", false
	}
	return key, true
}

// SplitBundleProfileRef splits a "<bundle>#profiles/<name>" reference into its
// bundle part and the bare profile name. ok is false when ref carries no
// "#profiles/" selector — e.g. a plain local profile name or a top-level
// "@profiles/" remote profile ref — so callers can tell a bundle-sourced profile
// apart from the other two and attribute it back to its bundle.
func SplitBundleProfileRef(ref string) (bundle, name string, ok bool) {
	ref = NormalizeRef(ref)
	i := strings.Index(ref, ProfileSelector)
	if i == -1 {
		return "", "", false
	}
	return ref[:i], ref[i+len(ProfileSelector):], true
}

// RetiredProfileSelector is the item-type segment of the RETIRED top-level
// profile distribution grammar ("<url>@profiles/<name>"). Top-level profile
// distribution was removed with ItemTypeProfile — profiles ship inside bundles
// (ProfileSelector) — but the segment constant survives so load-time migration
// (the profile upgrade pipeline) and sync collection can recognize the retired
// form and steer it to the bundle-profile grammar instead of treating it as an
// installable reference.
const RetiredProfileSelector = "@profiles/"

// SplitRetiredProfileRef reports whether ref is written in the retired
// top-level profile distribution grammar ("<url>@profiles/<name>[@version]")
// and splits it into the repo URL and bare profile name. Any trailing
// "@<version>" pin is dropped: the successor "<bundle>#profiles/<name>" grammar
// pins via the bundle's lockfile entry, not the ref. ok is false for anything
// else — the successor form (carries a "#" selector), non-canonical refs, and
// local names — so callers can hand any ref here safely.
func SplitRetiredProfileRef(ref string) (url, name string, ok bool) {
	ref = NormalizeRef(ref)
	if !IsCanonicalRef(ref) || strings.Contains(ref, "#") {
		return "", "", false
	}
	i := strings.Index(ref, RetiredProfileSelector)
	if i == -1 {
		return "", "", false
	}
	url, name = ref[:i], ref[i+len(RetiredProfileSelector):]
	if atIdx := strings.LastIndex(name, "@"); atIdx != -1 {
		name = name[:atIdx]
	}
	if url == "" || name == "" {
		return "", "", false
	}
	return url, name, true
}

// IsCanonicalRef checks if a reference is in canonical URL format.
func IsCanonicalRef(ref string) bool {
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}
