package remote

import (
	"strings"
)

// FragmentSelector is the selector prefix addressing a fragment within a
// bundle ("<bundle>#fragments/<name>"). Producers (bundle expansion) and
// consumers (reference parsing, exclusion matching) share this constant so
// the selector grammar lives in one place.
const FragmentSelector = "#fragments/"

// FragmentName returns the bare fragment name from a ref carrying a
// "#fragments/" selector; ok is false when ref has none.
func FragmentName(ref string) (name string, ok bool) {
	if i := strings.Index(ref, FragmentSelector); i != -1 {
		return ref[i+len(FragmentSelector):], true
	}
	return "", false
}

// splitItemPath separates a bundle reference's URL/name part from an optional
// "#item-path" suffix (e.g. "...#fragments/name"). When no suffix is present,
// itemPath is empty and base is the input unchanged.
func splitItemPath(ref string) (base, itemPath string) {
	if hashIdx := strings.Index(ref, "#"); hashIdx != -1 {
		return ref[:hashIdx], ref[hashIdx:]
	}
	return ref, ""
}

// CanonicalKey parses ref and returns its version-less canonical form — the
// key shape lockfiles and seeded-bundle maps use ("<url>@<kind>/<path>", or
// the ctxloom:local equivalent). ok is false when ref does not parse as a
// reference at all (e.g. a plain local bundle name).
func CanonicalKey(ref string) (string, bool) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return "", false
	}
	parsed.ContentVersion = ""
	return parsed.CanonicalString(), true
}

// LocalBundleRef returns the ctxloom:local canonical form for a plain local
// bundle name ("dev" → "ctxloom:local@bundles/dev").
func LocalBundleRef(name string) string {
	return LocalSource + "@bundles/" + name
}

// CanonicalBundleRef returns the canonical pipeline identity for a bundle
// reference: its version-less canonical form when it parses as a reference
// (remote URL or ctxloom:local), else the ctxloom:local form for a plain
// local bundle name. Every fragment name the assembly pipeline carries is
// "<CanonicalBundleRef>#fragments/<name>", so identities compare exactly and
// local/remote bundles with colliding names stay distinguishable.
func CanonicalBundleRef(name string) string {
	if ck, ok := CanonicalKey(name); ok {
		return ck
	}
	return LocalBundleRef(name)
}

// CanonicalFragmentRef canonicalizes the bundle part of a qualified fragment
// ref ("X#fragments/n" → "<CanonicalBundleRef(X)>#fragments/n"). Refs without
// a fragment selector (bare names) are returned unchanged. Any "@<commit>"
// content version is dropped — the canonical fragment identity is
// version-agnostic (see SplitFragmentVersion to keep the version).
func CanonicalFragmentRef(ref string) string {
	canonical, _ := SplitFragmentVersion(ref)
	return canonical
}

// SplitFragmentVersion canonicalizes the bundle part of a qualified fragment
// ref and splits off any "@<commit>" content version it carries:
// "X@<commit>#fragments/n" → ("<CanonicalBundleRef(X)>#fragments/n", "<commit>").
// An unversioned qualified ref yields an empty version; a ref without a fragment
// selector (a bare name) is returned unchanged with no version. The returned
// Name is the version-AGNOSTIC canonical identity (so dedup/exclusion/ordering
// stay version-agnostic); the version is meant to be honored only at the
// read/resolution path.
func SplitFragmentVersion(ref string) (canonical, version string) {
	base, sel := splitItemPath(ref)
	if !strings.HasPrefix(sel, FragmentSelector) {
		return ref, ""
	}
	if parsed, err := ParseReference(base); err == nil {
		version = parsed.ContentVersion
	}
	return CanonicalBundleRef(base) + sel, version
}

// PromptSelector is the selector prefix addressing a prompt within a bundle
// ("<bundle>#prompts/<name>"). The prompt counterpart to FragmentSelector;
// producers and consumers share it so the grammar lives in one place.
const PromptSelector = "#prompts/"

// SplitPromptVersion is the prompt counterpart to SplitFragmentVersion: it
// canonicalizes the bundle part of a qualified prompt ref and splits off any
// "@<commit>" content version it carries, whether the version sits on the
// bundle part ("X@<commit>#prompts/n") or trails the prompt name
// ("X#prompts/n@<commit>", the name-addressed CLI/resource form). The returned
// canonical is the version-AGNOSTIC identity ("<CanonicalBundleRef(X)>#prompts/n"),
// so the trust gate and dedup stay version-agnostic; the version is meant to be
// honored only at the read/resolution path (GetPromptAtVersion). A ref without a
// prompt selector (a bare name) is returned unchanged with no version — a bare
// name has no bundle to pin a historical version against.
func SplitPromptVersion(ref string) (canonical, version string) {
	base, sel := splitItemPath(ref)
	if !strings.HasPrefix(sel, PromptSelector) {
		return ref, ""
	}
	// Version on the bundle part: "X@<commit>#prompts/n".
	if parsed, err := ParseReference(base); err == nil {
		version = parsed.ContentVersion
	}
	// Version trailing the prompt name: "X#prompts/n@<commit>" (wins if present).
	if atIdx := strings.LastIndex(sel, "@"); atIdx != -1 {
		version = sel[atIdx+1:]
		sel = sel[:atIdx]
	}
	return CanonicalBundleRef(base) + sel, version
}

// IsCanonicalRef checks if a reference is in canonical URL format.
func IsCanonicalRef(ref string) bool {
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}
