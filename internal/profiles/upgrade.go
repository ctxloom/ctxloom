package profiles

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/upgrade"
)

// profileUpgrades is the canonical, ordered profile schema upgrade pipeline,
// oldest-first. Append an Upgrader here as the profile schema evolves; a
// Pipeline is itself an Upgrader, so the chain composes and the loader runs the
// whole thing over every profile (see loadFile).
//
// It is parameterized by the resolution context a bundle ref needs to be made
// canonical: ownURL is the repo URL of the remote the profile itself was
// installed from (so a bare sibling ref resolves against it), and aliasToURL
// maps any remote alias to its repo URL (so an "<alias>/<bundle>" ref resolves
// against the aliased repo). Both come from the load name + registry, which live
// outside the document, so the loader resolves them and threads them in.
// Remote-dependent upgrades self-gate when they have no resolution context (a
// local project profile has no remote), so the pipeline is always built — that
// keeps future remote-independent upgrades from being skipped for local profiles.
func profileUpgrades(ownURL string, aliasToURL func(string) string) upgrade.Pipeline {
	return upgrade.Pipeline{
		bundleRefCanonicalizeUpgrade{ownURL: ownURL, aliasToURL: aliasToURL},
	}
}

// bundleRefCanonicalizeUpgrade rewrites non-canonical bundle references to their
// canonical URL form ("<url>@bundles/<path>"). Early/legacy profiles listed
// bundles by bare name (`core-practices`) or by remote alias
// (`ctxloom-default/git`); but remote bundles are no longer extracted to disk —
// they are seeded and resolved by canonical ref ONLY (see
// config.loadRemoteBundleSeed). So neither short form resolves anymore. This
// canonicalizes each ref so the seeded resolver finds it unchanged, and — since
// loadFile records what it changed as a pending rewrite — migrates the on-disk
// profile to canonical storage on the next consented rewrite.
//
//   - A bare ref ("core-practices") resolves against ownURL (the profile's own
//     remote).
//   - An "<alias>/<bundle>" ref resolves against the alias' repo URL via
//     aliasToURL — including the common case where the alias is the profile's
//     own remote (a redundant prefix the old qualifier produced).
//   - An already-canonical ref is left untouched, so the upgrade is idempotent.
//   - A legacy ":fragments/…" / ":prompts/…" / ":mcp" item selector is rewritten
//     to the canonical "#…" form; a "#…" selector is preserved verbatim.
//   - Anything that cannot be resolved (no context, unknown alias, or a result
//     that fails to parse) is left unchanged — fault tolerant: persist the
//     authored form rather than drop the ref.
type bundleRefCanonicalizeUpgrade struct {
	ownURL     string
	aliasToURL func(string) string
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
	seq := upgrade.MapValue(root, key)
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
// "profiles/x") so the stored ref equals its canonical identity. The content
// version pin and item selector are preserved: CanonicalString drops the version
// (correct for a lockfile key, but a profile document must keep the pin). A
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
	normalized := parsed.CanonicalString()
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
	// internal/bundles/loader_content.go). Still re-normalize its layout so a
	// legacy "v1/" schema dir collapses to canonical storage.
	if remote.IsCanonicalRef(ref) {
		return renormalizeStoredRef(ref)
	}

	base, item := splitBundleSelector(ref)
	if base == "" {
		return ref, false
	}

	// Determine the source repo URL and the bundle path within it.
	var srcURL, bundlePath string
	if alias, rest, ok := strings.Cut(base, "/"); ok && rest != "" {
		// "<alias>/<bundle>": resolve the alias to its repo URL.
		if u.aliasToURL != nil {
			srcURL = u.aliasToURL(alias)
		}
		bundlePath = rest
	} else if !strings.Contains(base, "/") {
		// Bare "<bundle>": resolve against the profile's own remote.
		srcURL = u.ownURL
		bundlePath = base
	}
	if srcURL == "" || bundlePath == "" {
		return ref, false
	}

	canonical := srcURL + "@" + remote.ItemTypeBundle.DirName() + "/" + bundlePath + item
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
	for _, marker := range []string{":fragments/", ":prompts/", ":mcp"} {
		if i := strings.Index(ref, marker); i != -1 {
			// Drop the ':' and reintroduce the selector under the canonical '#'.
			return ref[:i], "#" + ref[i+1:]
		}
	}
	return ref, ""
}
