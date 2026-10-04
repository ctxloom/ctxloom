package profiles

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// decodeNormalizers are the context-free normalizer stages Decode runs over
// every profile document, oldest first. Each needs nothing beyond the
// document itself: the alias stage, which needs this machine's remote
// registry, runs in the loader and only over a LOCAL bundle's profiles
// (Loader.canonicalizeLocalAliases), and the retired-parent rewrite, which
// needs the whole seed, runs over the seed (RewriteRetiredParents).
var decodeNormalizers = upgrade.Pipeline{
	promptSelectorUpgrade{},
	bundleRefCanonicalizeUpgrade{},
}

// promptSelectorUpgrade rewrites legacy item selectors that targeted a bundle
// prompt ("<bundle>#prompts/<name>" or the ":prompts/" alias) to the commands
// section, matching the prompt→skill→command item-kind rename (the one-hop
// rewrite lands directly on "commands" — "skills" is reserved for a
// different, future item-kind, so a permanent rewrite can never target it).
// It runs BEFORE bundleRefCanonicalizeUpgrade so the ':' alias it preserves is
// then normalized to the canonical '#' form by that later stage. Idempotent:
// a ref already pointing at "#commands/"/":commands/" (or with no item
// selector) is untouched.
type promptSelectorUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (promptSelectorUpgrade) Name() string { return "rename prompt selectors to commands" }

// Apply rewrites prompt selectors in the bundles and bundle_items sequences.
func (u promptSelectorUpgrade) Apply(root *yaml.Node) bool {
	bundlesChanged := mapScalarSeq(root, "bundles", rewriteCommandSelector)
	itemsChanged := mapScalarSeq(root, "bundle_items", rewriteCommandSelector)
	return bundlesChanged || itemsChanged
}

// rewriteCommandSelector migrates a single ref's legacy prompt item selector
// to the commands section, preserving the selector's separator ('#' or ':').
// Returns the ref unchanged (false) when it carries no prompt selector.
func rewriteCommandSelector(ref string) (string, bool) {
	for _, sep := range []string{"#prompts/", ":prompts/"} {
		if strings.Contains(ref, sep) {
			return strings.Replace(ref, sep, sep[:1]+"commands/", 1), true
		}
	}
	return ref, false
}

// findBundleProfileKey returns the key in seeded for the profile shipped by
// repo url under the bare name, when exactly one bundle from that repo ships
// it. Ambiguity — two bundles from the same repo shipping the same profile
// name — yields false: a migration must not guess between them.
//
// A seeded key is the bundle's canonical identity plus "#profiles/<name>", and
// url is however the retired ref spelled the repository (https, scp, file), so
// the two are compared as REPOSITORIES — each parsed and normalized by the
// repo-URL grammar — never as string prefixes.
func findBundleProfileKey(seeded map[string]*Profile, url, name string) (string, bool) {
	repo, err := refuri.CanonicalRepoURL(url)
	if err != nil {
		return "", false
	}
	var match string
	for key := range seeded {
		bundle, profile, ok := remote.SplitBundleProfileRef(key)
		if !ok || profile != name {
			continue
		}
		ref, err := remote.ParseReference(bundle)
		if err != nil || ref.URL == "" {
			continue
		}
		if canon, cerr := refuri.CanonicalRepoURL(ref.URL); cerr != nil || canon != repo {
			continue
		}
		if match != "" {
			return "", false
		}
		match = key
	}
	return match, match != ""
}

// bundleRefCanonicalizeUpgrade rewrites non-canonical bundle references to their
// canonical URL form ("<url>@bundles/<path>"). Early/legacy profiles listed
// bundles by bare name (`core-practices`) or by remote alias
// (`ctxloom-default/git`); but remote bundles are no longer extracted to disk —
// they are seeded and resolved by canonical ref ONLY (see
// config.loadRemoteBundleSeed). This canonicalizes each ref, in memory, so the
// seeded resolver finds it unchanged. With no alias resolver (Decode's
// context-free run) only the context-free parts apply.
//
//   - A bare ref ("core-practices") names a LOCAL bundle and is left as
//     written.
//   - An "<alias>/<bundle>" ref resolves against the alias' repo URL via
//     aliasToURL — including the common case where the alias is the profile's
//     own remote (a redundant prefix the old qualifier produced) — through
//     remote.CanonicalizeShortRef, so a LOCAL bundle spelled the same way
//     (localBundleExists) stays local rather than being migrated to the remote.
//   - An already-canonical ref is left untouched, so the upgrade is idempotent.
//   - A legacy ":fragments/…" / ":commands/…" / ":mcp" item selector is rewritten
//     to the canonical "#…" form; a "#…" selector is preserved verbatim.
//   - Anything that cannot be resolved (no context, unknown alias, or a result
//     that fails to parse) is left unchanged — fault tolerant: persist the
//     authored form rather than drop the ref.
type bundleRefCanonicalizeUpgrade struct {
	aliasToURL        func(string) string
	localBundleExists func(string) bool
}

// Name identifies the upgrade in logs and the rewrite prompt.
func (bundleRefCanonicalizeUpgrade) Name() string { return "canonicalize bundle refs" }

// Apply canonicalizes the top-level `bundles` and `parents` sequences, reporting
// whether it changed anything. Bundles accept the legacy short/alias forms and
// are resolved to a repo URL; parents are only re-normalized when ALREADY
// canonical — a bare parent names a local sibling profile, and resolving it
// against a remote alias would silently turn a local parent into a remote ref.
// Both paths collapse a legacy "v1/" schema directory (stripLegacySchemaSegment)
// so a stored ref equals its own canonical identity.
func (u bundleRefCanonicalizeUpgrade) Apply(root *yaml.Node) bool {
	bundlesChanged := mapScalarSeq(root, "bundles", u.canonicalize)
	parentsChanged := mapScalarSeq(root, "parents", renormalizeStoredRef)
	return bundlesChanged || parentsChanged
}

// mapScalarSeq rewrites every scalar entry of the named top-level sequence via
// fn, replacing the value whenever fn reports a change. A missing or
// non-sequence node is a no-op. Returns whether any entry changed.
func mapScalarSeq(root *yaml.Node, key string, fn func(string) (string, bool)) (changed bool) {
	seq := yamlx.MapValue(root, key)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range seq.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		if rewritten, ok := fn(item.Value); ok {
			item.Value = rewritten
			changed = true
		}
	}
	return changed
}

// renormalizeStoredRef re-emits an already-canonical reference in normalized
// stored form, collapsing a legacy schema directory (e.g. "v1/profiles/x" →
// "profiles/x"). It re-renders the reference in the grammar it was AUTHORED in
// — its fetch address, "<url>@bundles/<path>" — rather than as its identity:
// this pass repairs a retired segment, it does not move a user's document onto
// the ctxloom+ URI grammar. The content version pin and item selector are
// preserved. A
// non-canonical or unparseable ref is returned verbatim, so it is safe to hand
// any parent ref here — bare local-sibling names and ctxloom:local refs are left
// untouched.
func renormalizeStoredRef(ref string) (string, bool) {
	if !remote.IsCanonicalRef(ref) {
		return ref, false
	}
	base, selector := splitBundleSelector(ref)
	parsed, err := remote.ParseReference(base)
	if err != nil {
		return ref, false
	}
	normalized := parsed.URL + "@" + remote.ItemTypeBundle.DirName() + "/" + parsed.Path
	if parsed.ContentVersion != "" {
		normalized += "@" + parsed.ContentVersion
	}
	normalized += selector
	if normalized == ref {
		return ref, false
	}
	return normalized, true
}

// canonicalize rewrites a single bundle ref to canonical URL form, reporting
// whether it changed. See the type doc for the resolution rules.
func (u bundleRefCanonicalizeUpgrade) canonicalize(ref string) (string, bool) {
	// A canonical URL ref is already fully qualified, so never re-resolve it —
	// its scheme colon ("https://") would otherwise be mistaken for the
	// cherry-pick ':' separator below, splitting the bundle name down to "https"
	// (the resolver's expandBundleRef hit the same trap; see
	// internal/core/bundles/loader_content.go). Still re-normalize its layout so a
	// legacy "v1/" schema dir collapses to canonical storage.
	if remote.IsCanonicalRef(ref) {
		return renormalizeStoredRef(ref)
	}

	base, item := splitBundleSelector(ref)
	if base == "" {
		return ref, false
	}

	if !strings.Contains(base, "/") {
		return ref, false
	}
	// "<alias>/<bundle>": the shared short-ref resolver, local-file-wins.
	resolved := remote.CanonicalizeShortRef(base, u.aliasToURL, u.localBundleExists)
	if !remote.IsCanonicalRef(resolved) {
		return ref, false
	}

	canonical := resolved + item
	// Validate before adopting: an unparseable result means our inputs didn't
	// compose into a real ref, so keep the authored form rather than corrupt it.
	if _, err := remote.ParseReference(canonical); err != nil {
		return ref, false
	}
	return canonical, true
}

// splitBundleSelector separates a bundle ref's bundle portion from an optional
// item selector, normalizing the legacy ':' selector to the canonical '#' form.
// The returned item, when present, includes its leading '#'. The bundle portion
// may itself contain '/' (an "<alias>/<bundle>" prefix); only the selector is
// split off here. A URL scheme colon is never mistaken for a selector because a
// ':' counts only when it introduces a known section (mirrors
// bundles.expandBundleRef).
func splitBundleSelector(ref string) (base, item string) {
	if i := strings.Index(ref, "#"); i != -1 {
		return ref[:i], ref[i:]
	}
	for _, marker := range []string{":fragments/", ":commands/", ":mcp"} {
		if i := strings.Index(ref, marker); i != -1 {
			// Drop the ':' and reintroduce the selector under the canonical '#'.
			return ref[:i], "#" + ref[i+1:]
		}
	}
	return ref, ""
}

// RewriteRetiredParents rewrites, in place, every seeded profile parent
// authored in the retired top-level "@profiles/" grammar to its bundle-shipped
// successor: the one seeded bundle profile the repo ships under that name,
// verbatim when unmatched or ambiguous. Seeded profiles arrive already parsed
// and never pass through a loader's document pipeline, so the seed applies the
// same rule (findBundleProfileKey) here. In-memory only: a seeded profile is
// read-only and migrates at its source.
func RewriteRetiredParents(seeded map[string]*Profile) {
	for _, p := range seeded {
		for i, parent := range p.Parents {
			if url, name, ok := remote.SplitRetiredProfileRef(parent); ok {
				if successor, found := findBundleProfileKey(seeded, url, name); found {
					p.Parents[i] = successor
				}
			}
		}
	}
}
