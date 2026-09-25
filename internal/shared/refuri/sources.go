package refuri

import "strings"

// The two SOURCE TOKENS that stand where a repository URL would: content
// authored in this project, and a loadout a companion binary emits. They are
// reference grammar, not fetch policy, which is why they live here below both
// the trust tier that keys on them and the fetcher that dispatches on them.

// LocalSource is the fixed source token for ctxloom:local references —
// project-authored content under the committed .ctxloom/content/ working copy.
// It mirrors the canonical grammar: LocalSource @ <type>/<path>[@version].
const LocalSource = "ctxloom:local"

// CompanionSource is the fixed source token for ctxloom:companion@<bin>
// references — a bundle emitted live by a companion binary discovered on
// PATH (`<bin> loadout --format json`, signature-envelope spec §4.3/§6
// discovery). This is the FIRST-CLASS, RECOGNIZED source token companion
// loadouts are seeded under: recognized here (so the unrecognized-source
// guard every caller builds on IsSelfContainedRef never fires for it) and mapped to a NON-local
// trust.Ref (Reference.IsLocal stays false), so companion content
// flows through EffectiveTrust's trusted-signer/approved/pending steps
// exactly like a remote bundle — never auto-allowed, never denied as
// unrecognized.
const CompanionSource = "ctxloom:companion"

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
//   - the whole canonical ctxloom+<class>: family, via refuri.HasScheme. Two
//     of the four classes are OPAQUE URIs — "ctxloom+local:x" carries no
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
	case HasScheme(ref),
		strings.HasPrefix(ref, LocalSource+"@"),
		strings.HasPrefix(ref, CompanionSource+"@"),
		// Any scp user, not just "git" — the sentinel arms above already
		// claim the "ctxloom:...@" spellings, which isSCPForm declines
		// anyway because their ":" precedes their "@". Testing "git@" alone
		// classified a gitolite or gerrit ref as a SHORT same-repo ref, so
		// it was expanded against the containing source instead of being
		// read as the remote it names.
		isSCPForm(ref),
		strings.Contains(ref, "://"):
		return true
	default:
		return false
	}
}
