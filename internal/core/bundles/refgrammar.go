package bundles

import (
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// The reference GRAMMAR the composer needs, served from this package: the
// bundle-reference grammar lives in adapters/remote, which this package
// already reaches (the pull walk is behind bundles.Reader); core/composite
// imports no adapter, so the few grammar questions selection asks are
// answered here.

// SplitFragmentVersion splits a fragment ask into its version-agnostic
// canonical identity and the "@<commit>" content version it pinned ("" when
// none). A bare name (no selector) is returned unchanged.
func SplitFragmentVersion(ref string) (canonical, version string, err error) {
	return remote.SplitFragmentVersion(ref)
}

// SplitCommandVersion is SplitFragmentVersion for a command ask.
func SplitCommandVersion(ref string) (canonical, version string, err error) {
	return remote.SplitPromptVersion(ref)
}

// FragmentSelector is the selector that addresses a fragment within a
// bundle ("<bundle>#fragments/<name>").
const FragmentSelector = remote.FragmentSelector

// Exclusions is a profile's exclude set, canonicalised so an exclusion meets
// the same ref spelled another way. An exclusion whose bundle part will not
// canonicalise is kept AS AUTHORED rather than dropped: dropping it would
// silently widen the context, and its own text is the only honest candidate.
type Exclusions struct{ set collections.Set[string] }

// NewExclusions canonicalises each exclusion.
func NewExclusions(refs []string) Exclusions {
	set := collections.NewSet[string]()
	for _, e := range refs {
		canonical, err := remote.CanonicalFragmentRef(e)
		if err != nil {
			canonical = e
		}
		set.Add(canonical)
	}
	return Exclusions{set: set}
}

// Excludes reports whether name is excluded: by its canonical ref, by its
// authored spelling, or by its bare fragment name.
func (e Exclusions) Excludes(name string) bool {
	if e.set == nil {
		return false
	}
	canonical, err := remote.CanonicalFragmentRef(name)
	if err != nil {
		canonical = name
	}
	if e.set.Has(canonical) {
		return true
	}
	if bare, ok := remote.FragmentName(canonical); ok {
		return e.set.Has(bare)
	}
	return false
}

// BundleSCM is the provenance stamp a bundle-shipped MCP server or hook
// carries (wire.MCPServer.SCM, wire.Hook.SCM): the identity of the bundle
// that shipped it. The resolver stamps it and the link grant reads it back,
// so "granted from THIS bundle" is one spelling.
func BundleSCM(src trust.BundleRef) string {
	return "bundle:" + string(src.BundleIdentity())
}
