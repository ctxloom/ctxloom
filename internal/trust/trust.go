// Package trust implements the per-item review-state store and the data model
// the trust decision function resolves over (trust-simplify). Every remote
// item (fragment, skill, MCP server, hook) is in exactly one of three states:
// pending (never reviewed, or changed since acceptance — withheld), accepted
// (a human reviewed this exact content, bound to its raw/distilled hash pair),
// or rejected (withheld permanently, with a content-hash denylist companion so
// a renamed identical copy stays rejected). First-party sources — local
// content, builtin bundles, and trusted sources (remotes.yaml TrustBundles) —
// are exempt from review; rejection beats even the first-party exemption.
//
// This package owns only the persistent store (afero-backed trust.yaml) plus
// the addressing/canonicalization primitives. The decision function itself
// lives in operations.EffectiveTrust, which unifies this store with the
// trusted-sources set at read time. The store never fetches or hashes content
// — callers compute the content hashes (see bundles.EffectiveContentHash) and
// pass them in.
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
	// SourceTrustedSource: the item's repo is in the trusted-sources set
	// (a registry remote carrying TrustBundles).
	SourceTrustedSource Source = "trusted-source"
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
)

// Dir returns the selector directory segment for the kind, matching the ref
// grammar: "<bundle>#fragments/<name>", "<bundle>#prompts/<name>",
// "<bundle>#mcp/<name>", "<bundle>#hooks/<event>/<index>".
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
	default:
		return string(k)
	}
}

// IsContent reports whether the kind is project-authorable *content* (fragment
// or prompt) as opposed to an executable surface (mcp / hook). It classifies the
// kind for content-vs-executable handling; it does NOT govern the local-tier
// auto-allow, which the decision function extends to all local kinds (a
// project-authored local mcp/hook is allowed too — see EffectiveTrust step 2).
func (k ItemKind) IsContent() bool {
	return k == KindFragment || k == KindPrompt
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

	// Kind is the item kind (fragment | prompt | mcp).
	Kind ItemKind

	// Name is the item name within the bundle, e.g. "solid".
	Name string

	// IsLocal marks a ctxloom:local (project-authored) item. Local content
	// (fragment/prompt) is auto-allowed; local executables (mcp) are not.
	IsLocal bool

	// IsBuiltin marks an item shipped inside the ctxloom binary itself
	// (resources/builtin_bundles). Mutually exclusive with IsLocal. Builtin
	// items key under BuiltinSigner (never remote.LocalSource) so they cannot
	// collide with a project-local bundle of the same name, and so a rejection
	// recorded against a builtin item is addressed unambiguously.
	IsBuiltin bool
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
func (r Ref) Key() string {
	return r.Bundle + "#" + r.Kind.Dir() + "/" + r.Name
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
// the same repo collapse to one key — otherwise a blacklist keyed on one
// spelling could be escaped by fetching the same repo under another. It builds
// on remote.NormalizeURL (unifies scheme, rewrites git@ → https, strips a
// trailing .git for http(s)) and then, for http(s) URLs, lowercases the host
// (DNS is case-insensitive) and — for known case-insensitive forges — the
// owner/repo path, and trims a trailing slash. Empty input and the
// ctxloom:local token pass through unchanged.
func CanonicalRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == remote.LocalSource {
		return remote.LocalSource
	}

	normalized := remote.NormalizeURL(raw)

	// Only http(s) URLs get host/path case folding; other transports (file://,
	// ssh://, git://) keep their path verbatim, where case may be significant.
	if !strings.HasPrefix(normalized, "http://") && !strings.HasPrefix(normalized, "https://") {
		return normalized
	}

	u, err := url.Parse(normalized)
	if err != nil {
		return normalized
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if knownCaseFoldForges[u.Host] {
		u.Path = strings.ToLower(u.Path)
	}
	return u.String()
}
