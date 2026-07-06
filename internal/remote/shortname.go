package remote

import "strings"

// CanonicalizeShortRef expands a per-remote short bundle reference —
// "<alias>/<bundle-path>[#<sel>/<item>]" — to its canonical
// "<url>@bundles/<bundle-path>[#<sel>/<item>]" form, resolving <alias> to a repo
// URL via aliasToURL against the remotes registry. It is the single widening of
// the resolver every short-name store site and the agents.*.profiles migration
// route through, so the grammar lives in exactly one place.
//
// Rules (the locked short-name decisions):
//
//   - An already scheme-qualified ref — a canonical URL (https://, git@, file://)
//     or a ctxloom:local ref — is self-contained and returned unchanged.
//   - A BARE name with NO "<alias>/" prefix ("foo", "foo#profiles/x",
//     "lang/go" only when the first segment names no configured remote) is LOCAL
//     by decision A/C: returned unchanged for the loader's local resolution. The
//     selector is peeled BEFORE the alias split so a selector-carrying bare ref
//     stays local.
//   - "<alias>/<bundle-path>[#<sel>/<item>]": local-file-wins (decision E) — when
//     localExists is non-nil and reports a local file for the base
//     ("<alias>/<bundle-path>"), the ref is LOCAL and returned unchanged. Otherwise
//     the alias resolves via aliasToURL; a hit expands to
//     "<url>@bundles/<bundle-path>[#<sel>/<item>]" with the selector preserved
//     VERBATIM (including any trailing "@<version>" pin), and a miss — unknown
//     alias or a nil resolver — returns the ref unchanged for the downstream
//     parser/loader to accept or reject.
//
// aliasToURL and localExists may each be nil: a nil aliasToURL disables remote
// resolution (everything stays as authored), a nil localExists skips the
// local-file tie-break (a pure store-side canonicalize with no fs view).
func CanonicalizeShortRef(ref string, aliasToURL func(alias string) string, localExists func(base string) bool) string {
	// Already self-contained (canonical URL or ctxloom:local) → as-is.
	if _, err := ParseReference(ref); err == nil {
		return ref
	}
	base, selector := SplitItemPath(ref)
	alias, path, ok := strings.Cut(base, "/")
	if !ok || alias == "" || path == "" {
		return ref // bare, unprefixed name → local (decision A/C)
	}
	if localExists != nil && localExists(base) {
		return ref // local-file-wins over a same-spelled remote alias (decision E)
	}
	if aliasToURL == nil {
		return ref
	}
	url := aliasToURL(alias)
	if url == "" {
		return ref // unknown alias → leave for the downstream parser/loader
	}
	return url + "@" + ItemTypeBundle.DirName() + "/" + path + selector
}

// CanonicalizeProfileShortRef canonicalizes a bundle-profile reference written in
// the per-remote short form "<alias>/<bundle>#profiles/<name>" to its canonical
// URL. A ref carrying no "#profiles/" selector is a local profile name (a bare
// name or a subdir profile like "team/dev") and is returned unchanged, so a
// selector-less two-segment name stays the local profile it names rather than
// resolving against a "team" remote (decisions A/E). Selector-carrying refs route
// through CanonicalizeShortRef; a local bundle-profile ("tools#profiles/probe")
// has no "<alias>/" prefix in its base and is likewise left local.
func CanonicalizeProfileShortRef(ref string, aliasToURL func(alias string) string) string {
	if _, _, ok := SplitBundleProfileRef(ref); !ok {
		return ref
	}
	return CanonicalizeShortRef(ref, aliasToURL, nil)
}
