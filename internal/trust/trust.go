// Package trust implements the per-item trust store and the data model the
// trust cascade resolves over (trust rework, TR1). Trust is expressible per
// fragment / prompt / mcp item, with the existing per-remote trust (kept in
// remotes.yaml as Remote.TrustBundles) and a per-bundle posture cascading to
// the items they contain.
//
// This package owns only the persistent store (afero-backed trust.yaml) plus
// the addressing/canonicalization primitives. The cascade itself lives in
// operations.EffectiveTrust, which unifies this store with the remote tier at
// read time. The store never fetches or hashes content — callers compute the
// effective-content hash (see bundles.EffectiveContentHash) and pass it in.
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

// Source names which cascade tier decided a trust evaluation. It is reported
// alongside the Decision so callers (and the list-JSON stamp in TR3) can
// explain why an item was allowed or withheld.
type Source string

const (
	// SourceDenylist: the content hash is on the repo/ref-agnostic denylist.
	SourceDenylist Source = "denylist"
	// SourceBlacklist: a sticky ref-level blacklist entry matched.
	SourceBlacklist Source = "blacklist"
	// SourceExplicitGrant: an explicit per-item trust grant matched the hash.
	SourceExplicitGrant Source = "explicit-grant"
	// SourceBundle: a SHA-agnostic bundle posture decided it.
	SourceBundle Source = "bundle"
	// SourceLocal: project-authored local content auto-allowed (fragment/prompt).
	SourceLocal Source = "local"
	// SourceRemote: the inherited remote.TrustBundles posture decided it.
	SourceRemote Source = "remote"
	// SourceDefault: nothing matched — the terminal fail-closed default-DENY.
	SourceDefault Source = "default"
)

// ItemKind distinguishes the trust-addressable item kinds. Only fragment and
// prompt are project-authored *content* that the local tier auto-allows; mcp is
// an executable surface that is never auto-trusted (bundle hooks are gated at
// their own choke in TR5 and follow the same executable treatment).
type ItemKind string

const (
	KindFragment ItemKind = "fragment"
	KindPrompt   ItemKind = "prompt"
	KindMCP      ItemKind = "mcp"
)

// Dir returns the selector directory segment for the kind, matching the ref
// grammar: "<bundle>#fragments/<name>", "<bundle>#prompts/<name>",
// "<bundle>#mcp/<name>".
func (k ItemKind) Dir() string {
	switch k {
	case KindFragment:
		return "fragments"
	case KindPrompt:
		return "prompts"
	case KindMCP:
		return "mcp"
	default:
		return string(k)
	}
}

// IsContent reports whether the kind is project-authorable *content* (fragment
// or prompt) as opposed to an executable surface (mcp). Only content kinds get
// the local-tier auto-allow.
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
}

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
