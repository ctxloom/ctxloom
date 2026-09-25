// Package trust implements the addressing/canonicalization primitives and the
// data model the trust decision function resolves over: every remote item
// (fragment, command, MCP server, hook, Agent Skill) is in exactly one of
// three states —
// pending (never reviewed, or changed since approval — withheld), accepted (a
// human COUNTERSIGNED this exact content with their own SSH key — see
// internal/adapters/signing/countersign), or rejected (withheld permanently; the
// rejection is itself a countersignature, and a content-scoped rejection
// deliberately omits the ref so a renamed identical copy stays rejected —
// signature-envelope spec §5.3). First-party sources — local content,
// companion loadouts, and content from a trusted PUBLISHER (a signing key in
// allowed_signers, verified over the bytes) — are exempt from review;
// rejection beats even the first-party exemption.
//
// This package owns the addressing (Ref) and canonicalization
// (CanonicalRepoURL) primitives and declares the three PORTS the decision is
// made with (TrustRoot, ReviewRecords, RetractionRecords — see ports.go); it
// holds no persisted state of its own. The decision cascade lives in
// composite (composite.NewTrust over the three ports; the adapters under
// internal/adapters/signing and the lockfile implement them). Nothing here
// fetches, hashes, or signs content — callers pass in the exact bytes (see
// bundles.ContentPayload) and this package never touches them.
package trust

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
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
	// every exemption, including local/companion/trusted-source.
	SourceRejected Source = "rejected"
	// SourceLocal: project-authored local content auto-allowed (all kinds).
	SourceLocal Source = "local"
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
	// It is a DISTINCT step below rejection, precisely so step 1 still
	// reaches it.
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
	// version of it) via its remote manifest — see internal/adapters/remote/retract.go
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
	// trust.ParseSelector).
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

// ParseItemKind is the one entry from a runtime string into the closed
// core: a spelling that names none of ItemKinds is refused.
func ParseItemKind(s string) (ItemKind, bool) {
	return collections.Member(ItemKinds(), s)
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

	// IsCompanion marks an item from a companion binary's own loadout
	// (ctxloom:companion@<bin>). It is a distinct, nameable
	// exemption step in the decision function (SourceCompanion) rather than a
	// second spelling of IsLocal, so step 1's rejection check still runs ahead
	// of it and a reader can see WHICH exemption allowed an item.
	//
	// Every production site that sets it
	// copies remote.Reference.IsCompanion, which the reference grammar sets
	// only for the fixed refuri.CompanionSource token — never for a URL or
	// bundle name an author can choose — and which never coincides with
	// IsLocal (pinned by remote's own reference_companion_test). The Ref keys
	// under that same token, so a companion item cannot collide with a
	// project-local or remote bundle.
	IsCompanion bool
}

// Key returns the repo-relative item key used in the store, e.g.
// "code-quality#fragments/solid" or "tooling#mcp/postgres". It deliberately
// omits the repo URL (stored separately) and any @version (grants pin by
// content hash, not commit).
//
// This is the ingest boundary for Bundle and Name, and the LAST one: a Ref is a
// plain struct, so those two fields are set directly by every surface type's
// RefFor (internal/adapters/content) from a bundle-manifest item name or a filename —
// neither of which passes through the reference grammar in internal/adapters/remote.
// Key is the single function that turns those fields into a ref string, and
// operations.countersignRef composes its result straight into the countersign
// preimage, where a control character forges the frame (see
// signing.CountersignHeader.Validate). Normalising here covers every
// construction site at once, including ones not yet written.
func (r Ref) Key() string {
	return refuri.NormalizeRef(r.Bundle) + "#" + r.Kind.Dir() + "/" + refuri.NormalizeRef(r.Name)
}

// CanonicalURL returns the canonical repo URL (refuri.CanonicalRepoURL) for
// display. Local items render the fixed ctxloom:local source token so they
// never collide with a remote and are distinguishable from an unresolved
// (empty-URL) remote ref. A RepoURL that does not canonicalize renders "": it
// names no repository, and AsBundleRef refuses the same Ref for the same
// reason.
func (r Ref) CanonicalURL() string {
	if r.IsLocal {
		return refuri.LocalSource
	}
	canon, err := refuri.CanonicalRepoURL(r.RepoURL)
	if err != nil {
		return ""
	}
	return canon
}

// AsBundleRef converts r into the canonical bundle-reference grammar
// (BundleRef), minting through the same GitRef / FileRef /
// LocalRef / CompanionRef entry points a caller building a fresh reference by
// hand would use — so a Ref converted here is held to exactly the rules
// (R1-R4) a hand-typed reference is, never a laxer path around them.
//
// It exists so operations.countersignRef can key the countersignature store
// on BundleRef.Identity() instead of the retired CanonicalURL()+"|"+Key()
// composition (R5): see bundleref.go's Identity doc for why that join was
// itself a framing hazard.
//
// r.RepoURL is read by refuri.ParseRepoIdentity, the one repo-level
// canonicalizer, so a Ref built from any transport spelling of a repository
// (a git@ remote, an http URL, a trailing slash, mixed host case) converts to
// the same BundleRef a hand-typed reference to it parses to. It folds nothing
// that is only PROBABLY the same repository — a ".git" suffix, a "www." host
// and repository-path case all survive as distinct identities — and a RepoURL
// it cannot read is an error, never a key minted for the raw string. A refusal
// here is not a diagnostic the user ever sees: the caller degrades to an
// inert, non-colliding address (see countersignRef) and the item is silently
// WITHHELD from delivery.
func (r Ref) AsBundleRef() (BundleRef, error) {
	base, err := r.bundleRefBase()
	if err != nil {
		return BundleRef{}, err
	}
	if r.Kind == "" && r.Name == "" {
		return base, nil
	}
	return base.WithItem(r.Kind, r.Name)
}

// bundleRefBase converts everything about r EXCEPT its item selector — the
// half AsBundleRef shares with a future bundle-only (no item) conversion.
func (r Ref) bundleRefBase() (BundleRef, error) {
	switch {
	case r.IsLocal:
		return LocalRef(r.Bundle)
	case r.IsCompanion:
		return CompanionRef(r.Bundle)
	}

	repo, err := refuri.ParseRepoIdentity(r.RepoURL)
	if err != nil {
		return BundleRef{}, err
	}
	switch repo.Class {
	case ClassLocal:
		return LocalRef(r.Bundle)
	case ClassCompanion:
		return CompanionRef(r.Bundle)
	case ClassFile:
		return FileRef(repo.RepoPath, r.Bundle)
	}
	// Every other transport (https, ssh://, git://, scp) addresses a bundle "in
	// a remote git repository reachable by host" — ClassGit abstracts away
	// which transport reached it.
	return GitRef(repo.Host, repo.RepoPath, r.Bundle)
}

// RefFromBundleRef is AsBundleRef's mechanical inverse: it maps a BundleRef's
// structured fields back onto the wider trust.Ref shape. br has already
// passed ParseBundleRef's rules (it came from ParseBundleRef itself or from a
// minter), so this is a field mapping, not a validation — no parsing, no I/O.
//
// The one direction that is not a bare field copy is ClassGit/ClassFile's
// Host+RepoPath -> RepoURL, which is BundleRef.FetchURL — the one reverse
// renderer, whose output ParseRepoIdentity reads back to the same fields.
//
// A companion's RepoURL is stamped to refuri.CompanionSource rather than left
// empty: Ref.CanonicalURL has no IsCompanion branch of its own and falls
// through to CanonicalRepoURL(r.RepoURL), which recognizes that exact token —
// so a round-tripped companion Ref must carry it to key the same way one
// built through remote.ParseReference does.
func RefFromBundleRef(br BundleRef) Ref {
	r := Ref{Bundle: br.Bundle, Kind: br.Kind, Name: br.Item}
	switch br.Class {
	case ClassLocal:
		r.IsLocal = true
	case ClassCompanion:
		r.IsCompanion = true
		r.RepoURL = refuri.CompanionSource
	case ClassGit, ClassFile:
		r.RepoURL = br.FetchURL()
	}
	return r
}

// DisplayRef renders r in the canonical bundle-reference grammar (BundleRef)
// for display — an advisory, a listing, a diagnostic.
//
// It returns an ERROR rather than a stand-in string when r cannot convert (see
// AsBundleRef's doc for when that happens): a display string shaped like a
// reference but addressing nothing is a counterfeit identity, and a caller
// that cannot tell the two apart will key on it.
//
// Unlike operations.CountersignRef (which keys the countersignature store on
// Identity, version-less by design), DisplayRef renders String — including
// "@<version>" when the Ref carries one — because a display string is for a
// human to read or re-type, and a version pinned in the original ref is part
// of what they typed.
func (r Ref) DisplayRef() (string, error) {
	br, err := r.AsBundleRef()
	if err != nil {
		return "", fmt.Errorf("cannot address %s#%s/%s: %w", r.Bundle, r.Kind.Dir(), r.Name, err)
	}
	return br.String(), nil
}
