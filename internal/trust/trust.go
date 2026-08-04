// Package trust implements the addressing/canonicalization primitives and the
// data model the trust decision function resolves over: every remote item
// (fragment, command, MCP server, hook, Agent Skill) is in exactly one of
// three states —
// pending (never reviewed, or changed since approval — withheld), accepted (a
// human COUNTERSIGNED this exact content with their own SSH key — see
// internal/signing/countersign), or rejected (withheld permanently; the
// rejection is itself a countersignature, and a content-scoped rejection
// deliberately omits the ref so a renamed identical copy stays rejected —
// signature-envelope spec §5.3). First-party sources — local content, builtin
// bundles, and content from a trusted PUBLISHER (a signing key in
// allowed_signers, verified over the bytes) — are exempt from review;
// rejection beats even the first-party exemption.
//
// This package owns ONLY the addressing (Ref) and canonicalization
// (CanonicalRepoURL) primitives — it holds no persisted state of its own. The
// decision function lives in operations.EffectiveTrust, which resolves the
// countersignature stores (operations.ReviewRecords, backed by
// internal/signing/countersign) together with the verified publisher signer.
// Nothing here fetches, hashes, or signs content — callers pass in the exact
// bytes (see bundles.ContentPayload) and this package never touches them.
package trust

import (
	"net/url"
	"strings"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// Decision is the outcome of a trust evaluation.
type Decision string

const (
	// Allow exposes the item to the agent.
	Allow Decision = "allow"
	// Deny withholds the item. Deny is the fail-closed default — any path that
	// cannot positively justify exposure resolves to Deny.
	Deny Decision = "deny"
)

// Source names which decision-function step decided a trust evaluation. It is
// reported alongside the Decision so callers (and the list-JSON stamp) can
// explain why an item was allowed or withheld.
type Source string

const (
	// SourceRejected: a human declined this item (ref-level rejected state) or
	// its content hash is on the repo/ref-agnostic denylist. Rejection beats
	// every exemption, including local/builtin/trusted-source.
	SourceRejected Source = "rejected"
	// SourceLocal: project-authored local content auto-allowed (all kinds).
	SourceLocal Source = "local"
	// SourceBuiltin: compiled into this binary (resources/builtin_bundles).
	// Authenticated by the binary itself — trusting ctxloom trusts what it
	// ships — and allowed by default with no review friction, but (unlike the
	// old gate=nil bypass) reachable by SourceRejected: a user can reject a
	// builtin item and have that rejection enforced.
	SourceBuiltin Source = "builtin"
	// SourceCompanion: a loadout an installed companion binary advertised about
	// itself (ctxloom:companion@<bin>). LOCAL-EQUIVALENT, and for a reason that
	// is about ORDER OF OPERATIONS, not about deference to the companion:
	// ctxloom reads a loadout by EXECUTING the binary (`<bin> loadout --format
	// json`), so by the time the content exists that binary has already run
	// arbitrary code as the user. Reviewing the CONTENT afterwards buys ~nothing
	// while costing a review prompt for a tool the user deliberately installed.
	// The meaningful control point is therefore EXEC, and that is where the
	// human decision moved (config.AdmitCompanions: trust-on-first-use keyed on
	// absolute path + binary hash; first-party names exempt only when they
	// resolve from ctxloom's own install directory).
	//
	// Like SourceBuiltin this is a DISTINCT step below rejection, precisely so
	// step 1 still reaches it.
	//
	// A companion's SIGNATURE does not enter this decision in either direction.
	// A publisher signature protects bytes from an intermediary, and a loadout
	// has none — it arrives on the stdout of a binary the user already
	// consented to run. So a signature that fails to verify at that seam is a
	// stale or mismatched signature in the companion's own release, and
	// config.ProbeCompanionLoadouts reports it and delivers the content
	// unattributed rather than withholding.
	SourceCompanion Source = "companion"
	// SourceRetracted: the PUBLISHER withdrew this bundle (or this exact
	// version of it) via its remote manifest — see internal/remote/retract.go
	// CheckRetracted. Retraction is recorded LOCALLY at sync time (sync has the
	// network in hand) and consulted here with no network call of its own;
	// like rejection, it beats every exemption below, including a trusted
	// signer's own key — a publisher can retract content signed by a key this
	// machine still trusts.
	SourceRetracted Source = "retracted"
	// SourceTrustedSigner: the item's bundle carries a VERIFIED publisher
	// signature by a key this machine trusts for the publish namespace
	// (allowed_signers). Trust is keyed to the signing IDENTITY, not to the
	// repo the bytes arrived from: a fork, a typosquat, a compromised forge, or
	// a tampered clone object cannot produce content that verifies under the key
	// you actually trusted.
	//
	// This REPLACES the deleted trusted-source (remotes.yaml trust_bundles)
	// step, which trusted a LOCATION and was hash-blind — a compromised URL
	// could serve changed content forever and the gate would pass it.
	SourceTrustedSigner Source = "trusted-signer"
	// SourceAccepted: a human accepted this item and the recorded hash for the
	// current effective form matches the recomputed content hash.
	SourceAccepted Source = "accepted"
	// SourcePending: nothing positively justified exposure — the item awaits
	// review. This is also the terminal fail-closed source (unreadable store or
	// registry, unresolvable ref/hash).
	SourcePending Source = "pending"
)

// State is an item's review state in the three-state model. Pending is the
// implicit state of any item with no store entry; only accepted and rejected
// are persisted.
type State string

const (
	// StatePending: never reviewed, or content changed since acceptance.
	StatePending State = "pending"
	// StateAccepted: a human reviewed this exact content (hash-pair bound).
	StateAccepted State = "accepted"
	// StateRejected: a human declined it — withheld permanently.
	StateRejected State = "rejected"
)

// ItemKind distinguishes the trust-addressable item kinds. The local tier of
// the decision function (EffectiveTrust step 2) auto-allows ALL kinds when the
// item is project-authored — fragment and prompt content AND the mcp/hook
// executable surfaces the user configured in this project themselves. IsContent
// only names which kinds are project-authorable *content* (fragment/prompt) vs
// executable surfaces (mcp/hook); it no longer governs the local auto-allow.
// Remote mcp/hook still gate at their exposure chokes (MCP-server resolver,
// bundle-hook resolver) until reviewed — only locality, not kind, exempts them.
type ItemKind string

const (
	KindFragment ItemKind = "fragment"
	KindPrompt   ItemKind = "prompt"
	KindMCP      ItemKind = "mcp"
	KindHook     ItemKind = "hook"
	// KindSkill is a true Agent Skill package (a SKILL.md directory tree,
	// Part B2 of the skill/command split) — a different concept from
	// KindPrompt (the user-invoked slash-command item, historically also
	// called "skill" before the Part A rename). Its selector directory
	// "skills" was freed for this meaning by that rename; nothing production
	// still resolves "#skills/<name>" to KindPrompt (see
	// operations.parseTrustSelector).
	KindSkill ItemKind = "skill"
)

// ItemKinds returns every item kind declared here, in a fixed order.
//
// It exists so exhaustiveness over the kinds is TESTABLE rather than trusted:
// anything that must handle every kind (in particular the derivation of the
// attestation form a countersignature binds) has a test that walks this list, so
// a kind added here without being handled there fails that test instead of
// surfacing at runtime as an item nobody can approve.
//
// The vocabulary is open at the surface-type registry (a registered kind may be
// declared outside this package — content.KindProfile is one), which is exactly
// why this list is the CLOSED core rather than a claim of completeness.
func ItemKinds() []ItemKind {
	return []ItemKind{KindFragment, KindPrompt, KindMCP, KindHook, KindSkill}
}

// Dir returns the selector directory segment for the kind, matching the ref
// grammar: "<bundle>#fragments/<name>", "<bundle>#prompts/<name>",
// "<bundle>#mcp/<name>", "<bundle>#hooks/<event>/<index>",
// "<bundle>#skills/<name>".
func (k ItemKind) Dir() string {
	switch k {
	case KindFragment:
		return "fragments"
	case KindPrompt:
		return "prompts"
	case KindMCP:
		return "mcp"
	case KindHook:
		return "hooks"
	case KindSkill:
		return "skills"
	default:
		return string(k)
	}
}

// IsContent reports whether the kind is project-authorable *content*
// (fragment, prompt, or skill) as opposed to an executable surface (mcp /
// hook). A skill counts as content here even though its scripts/ files are
// executable: unlike mcp/hook (which have no bytes worth diffing — review
// always renders their full surface), a skill IS a reviewable file tree, and
// this flag is what lets review_snapshots.go cache its rendered tree text for
// a later diff. It does NOT govern the local-tier auto-allow, which the
// decision function extends to all local kinds (a project-authored local
// mcp/hook is allowed too — see EffectiveTrust step 2).
func (k ItemKind) IsContent() bool {
	return k == KindFragment || k == KindPrompt || k == KindSkill
}

// Ref addresses a trust-evaluable item: its source repo, the repo-relative
// bundle path, the item kind, and the item name. It is the in-memory shape the
// resolver and mutations key on; the persisted key is (CanonicalRepoURL, Key).
type Ref struct {
	// RepoURL is the source repository URL (empty for local items). It is
	// canonicalized via CanonicalRepoURL before keying so URL variants
	// (scheme, .git, case, git@ vs https) cannot escape a blacklist.
	RepoURL string

	// Bundle is the repo-relative bundle path, e.g. "code-quality" — NOT the
	// full canonical ref. This is the bundle component of the stored ref key.
	Bundle string

	// Kind is the item kind (fragment | prompt | mcp | hook | skill) -- see ItemKind.
	Kind ItemKind

	// Name is the item name within the bundle, e.g. "solid".
	Name string

	// IsLocal marks a ctxloom:local (project-authored) item. EVERY local kind
	// is auto-allowed at the decision function's local tier — fragment,
	// prompt and skill content AND the mcp/hook executable surfaces the user
	// configured in this project themselves. Kind does not enter that tier at
	// all: only locality exempts, which is why IsContent (below) explicitly
	// does not govern it. See ItemKind's own doc, EffectiveTrust step 3, and
	// the "local mcp auto-allowed (project-authored executable)" case in
	// operations' decision-function table.
	IsLocal bool

	// IsBuiltin marks an item shipped inside the ctxloom binary itself
	// (resources/builtin_bundles). Mutually exclusive with IsLocal — every
	// site that builds a Ref sets at most one of the two as a literal, and the
	// single site that copies both from data (content.Provenance.stamp) is
	// refused at construction by Provenance.validate, "provenance cannot be
	// both local and builtin". Neither field is assigned anywhere else, so the
	// exclusion is a checked property rather than a convention. It matters
	// because the two flags are read at DIFFERENT layers: CanonicalURL keys
	// builtin first, while the decision function reaches its local tier first,
	// so a Ref carrying both would key under one identity and report the
	// other. Builtin
	// items key under BuiltinSigner (never remote.LocalSource) so they cannot
	// collide with a project-local bundle of the same name, and so a rejection
	// recorded against a builtin item is addressed unambiguously.
	IsBuiltin bool

	// IsCompanion marks an item from a companion binary's own loadout
	// (ctxloom:companion@<bin>). Like IsBuiltin it is a distinct, nameable
	// exemption step in the decision function (SourceCompanion) rather than a
	// second spelling of IsLocal, so step 1's rejection check still runs ahead
	// of it and a reader can see WHICH exemption allowed an item.
	//
	// The single production site that sets it (operations.parseTrustItemRef)
	// copies remote.Reference.IsCompanion, which the reference grammar sets
	// only for the fixed remote.CompanionSource token — never for a URL or
	// bundle name an author can choose — and which never coincides with
	// IsLocal (pinned by remote's own reference_companion_test). The Ref keys
	// under that same token, so a companion item can no more collide with a
	// project-local or remote bundle than a builtin can.
	IsCompanion bool
}

// BuiltinSigner is the synthetic identity builtin items key under —
// distinct from remote.LocalSource, so a builtin bundle can never collide
// with a project-local bundle sharing its name. It names WHO vouches for the
// content (the ctxloom binary itself), matching the identity a future signed
// builtin loadout would carry — it is a plain identity string here, not a
// cryptographic signer; no signature is verified.
const BuiltinSigner = "builtin:ctxloom"

// Key returns the repo-relative item key used in the store, e.g.
// "code-quality#fragments/solid" or "tooling#mcp/postgres". It deliberately
// omits the repo URL (stored separately) and any @version (grants pin by
// content hash, not commit).
//
// This is the ingest boundary for Bundle and Name, and the LAST one: a Ref is a
// plain struct, so those two fields are set directly by every surface type's
// RefFor (internal/content) from a bundle-manifest item name or a filename —
// neither of which passes through the reference grammar in internal/remote.
// Key is the single function that turns those fields into a ref string, and
// operations.countersignRef composes its result straight into the countersign
// preimage, where a control character forges the frame (see
// signing.CountersignHeader.Validate). Normalising here covers every
// construction site at once, including ones not yet written.
func (r Ref) Key() string {
	return remote.NormalizeRef(r.Bundle) + "#" + r.Kind.Dir() + "/" + remote.NormalizeRef(r.Name)
}

// CanonicalURL returns the canonical repo URL used for keying. Local items key
// under the fixed ctxloom:local source token so they never collide with a
// remote and are distinguishable from an unresolved (empty-URL) remote ref.
func (r Ref) CanonicalURL() string {
	if r.IsBuiltin {
		return BuiltinSigner
	}
	if r.IsLocal {
		return remote.LocalSource
	}
	return CanonicalRepoURL(r.RepoURL)
}

// knownCaseFoldForges are forges whose owner/repo path is case-insensitive, so
// canonicalization may safely lowercase the whole path. Other hosts (and
// file:// paths) keep their path case, which can be significant.
var knownCaseFoldForges = map[string]bool{
	"github.com":     true,
	"www.github.com": true,
	"gitlab.com":     true,
	"bitbucket.org":  true,
}

// CanonicalRepoURL canonicalizes a repository URL so that variant spellings of
// the same repo collapse to one key — otherwise a rejection keyed on one
// spelling could be escaped by fetching the same repo under another. This is
// the ENTIRE defense against that escape: the countersignature store's address
// is CanonicalURL()+"|"+Key(), so any divergence between two spellings is not
// a near miss, it is a store miss — and for a bundle with a verified publisher
// signature the escape is not "rejected → pending" but "rejected → ALLOW" at
// step 5. docs/trust-model.md lists "URL-variant / typosquat escape of a
// rejection" as an addressed threat; this function is where that claim is
// either true or false.
//
// It builds on remote.NormalizeURL (unifies scheme, rewrites git@ → https,
// strips a trailing .git for http(s)) and then, for http(s) URLs: normalizes
// http → https, lowercases the host, folds a www. prefix off known forges,
// drops userinfo/query/fragment, trims trailing slashes and a .git suffix in
// that order, and lowercases the owner/repo path on known case-insensitive
// forges. Empty input, the ctxloom:local token, and the ctxloom:companion
// token pass through unchanged.
//
// Anything ADDED here must be a spelling of the same repository, never a
// different one: folding two distinct repos onto one key would let a rejection
// of one silently block — or an approval of one silently allow — the other.
func CanonicalRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == remote.LocalSource {
		return remote.LocalSource
	}
	if raw == remote.CompanionSource {
		// A companion loadout's RepoURL is the fixed CompanionSource token
		// (differentiated by Bundle=<bin>, mirroring how every local bundle
		// shares remote.LocalSource) — never a real URL. Without this early
		// return, remote.NormalizeURL's "no scheme, no slash" fallback would
		// mangle it into "https://ctxloom:companion", the same bug the
		// ctxloom:local case above exists to avoid.
		return remote.CompanionSource
	}

	normalized := remote.NormalizeURL(raw)

	// PARSE FIRST, then branch on the parsed scheme. The old order
	// was a HasPrefix check on the raw string, which is case-SENSITIVE: an
	// uppercase "HTTPS://github.com/acme/repo" skipped host folding, path
	// folding and slash trimming altogether — every one of them — even though
	// url.Parse lowercases the scheme for free. Deciding on the parsed value
	// removes a whole class of "the guard did not recognize its own input".
	u, err := url.Parse(normalized)
	if err != nil {
		return normalized
	}

	// Only http(s) URLs get host/path folding; other transports (file://,
	// ssh://, git://) keep their path verbatim, where case may be significant.
	switch u.Scheme {
	case "http":
		// A rejection recorded over https must not be escaped by refetching
		// the same repo over http. The transport is not part of the repo's
		// identity, and no forge serves different content on the two.
		u.Scheme = "https"
	case "https":
	default:
		return normalized
	}

	u.Host = strings.ToLower(u.Host)
	// www.github.com and github.com are one repo. knownCaseFoldForges already
	// listed "www.github.com", which proves the variant was considered — but
	// nothing ever rewrote the host, so the entry only ever lowercased the
	// path of a URL that still compared unequal to its bare-host twin.
	// Folding the prefix off is what makes the entry mean something.
	if strings.HasPrefix(u.Host, "www.") && knownCaseFoldForges[strings.TrimPrefix(u.Host, "www.")] {
		u.Host = strings.TrimPrefix(u.Host, "www.")
	}

	// Credentials, query and fragment address a REQUEST, never a repository.
	// Leaving them in made "…/repo?ref=x" a different trust key from "…/repo".
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""

	// Trim trailing slashes BEFORE stripping .git, and strip .git HERE rather
	// than relying on remote.NormalizeURL. NormalizeURL's TrimSuffix(".git")
	// runs before this function ever sees the string, so "…/repo.git/" kept
	// its ".git" — connascence of order across two packages, where neither
	// side is wrong on its own. Doing both, in this order, in one place makes
	// the function total over the suffix spellings.
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, ".git")
	u.Path = strings.TrimRight(u.Path, "/")
	if knownCaseFoldForges[u.Host] {
		u.Path = strings.ToLower(u.Path)
	}
	return u.String()
}
