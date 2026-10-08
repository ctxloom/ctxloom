// Package ident owns the addressing and canonicalization primitives every
// bundle item is identified by: the item kinds (ItemKind), the item reference
// (Ref), the canonical bundle-reference grammar (BundleRef) and the content
// forms an item is served in (ContentForm). It holds no persisted state of its
// own; nothing here fetches or hashes content.
package ident

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// ContentForm identifies which materialization of an item's content was
// served: the raw authored bytes, or the distilled rewrite.
type ContentForm string

const (
	FormRaw       ContentForm = "raw"
	FormDistilled ContentForm = "distilled"
	// FormNone is the absence of a form: an item that binds no content body.
	FormNone ContentForm = ""
)

// ItemKind distinguishes the addressable item kinds.
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
	// ident.ParseSelector).
	KindSkill ItemKind = "skill"
)

// ItemKinds returns every item kind declared here, in a fixed order.
//
// It exists so exhaustiveness over the kinds is TESTABLE rather than trusted:
// anything that must handle every kind has a test that walks this list.
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

// Noun is the name a user knows the kind by — the word the CLI, search's
// types and any message to a human or agent use. It differs from the stored
// spelling for one kind: a slash command is a "command", stored under
// KindPrompt so existing grants survive the item-kind rename. A kind outside
// the closed core renders as itself.
func (k ItemKind) Noun() string {
	if k == KindPrompt {
		return "command"
	}
	return string(k)
}

// Ref addresses an item: its source repo, the repo-relative bundle path, the
// item kind, and the item name.
type Ref struct {
	// RepoURL is the source repository URL (empty for local items). It is
	// canonicalized via CanonicalRepoURL before keying so transport variants
	// (scheme, git@ vs https, credentials, host case, default port) name one
	// repository; a ".git" suffix and repository-path case are identity and
	// are preserved (see refuri.Parse).
	RepoURL string

	// Bundle is the repo-relative bundle path, e.g. "code-quality" — NOT the
	// full canonical ref.
	Bundle string

	// Kind is the item kind (fragment | prompt | mcp | hook | skill) -- see ItemKind.
	Kind ItemKind

	// Name is the item name within the bundle, e.g. "solid".
	Name string

	// IsLocal marks a ctxloom:local (project-authored) item.
	IsLocal bool

	// IsCompanion marks an item from a companion binary's own loadout
	// (ctxloom:companion@<bin>), distinct from IsLocal.
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

// Key returns the repo-relative item key, e.g. "code-quality#fragments/solid"
// or "tooling#mcp/postgres". It deliberately omits the repo URL and any
// @version.
//
// This is the ingest boundary for Bundle and Name, and the LAST one: a Ref is a
// plain struct, so those two fields are set directly by every surface type's
// RefFor (internal/adapters/content) from a bundle-manifest item name or a filename —
// neither of which passes through the reference grammar in internal/adapters/remote.
// Key is the single function that turns those fields into a ref string, so
// normalising here covers every construction site at once, including ones not
// yet written.
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
// r.RepoURL is read by refuri.ParseRepoIdentity, the one repo-level
// canonicalizer, so a Ref built from any transport spelling of a repository
// (a git@ remote, an http URL, a trailing slash, mixed host case) converts to
// the same BundleRef a hand-typed reference to it parses to. It folds nothing
// that is only PROBABLY the same repository — a ".git" suffix, a "www." host
// and repository-path case all survive as distinct identities — and a RepoURL
// it cannot read is an error, never a key minted for the raw string.
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
// structured fields back onto the wider ident.Ref shape. br has already
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
// DisplayRef renders String — including "@<version>" when the Ref carries one
// — because a display string is for a human to read or re-type, and a version
// pinned in the original ref is part of what they typed.
func (r Ref) DisplayRef() (string, error) {
	br, err := r.AsBundleRef()
	if err != nil {
		return "", fmt.Errorf("cannot address %s#%s/%s: %w", r.Bundle, r.Kind.Dir(), r.Name, err)
	}
	return br.String(), nil
}
