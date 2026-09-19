package config

import (
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// NewExclusionSet builds the match set for exclude_fragments entries. Bare
// names are kept verbatim — a bare exclusion matches its fragment name in
// any bundle, by design (the inherit-three-bundles case shouldn't need three
// qualified exclusions). Qualified refs canonicalize their bundle part
// (remote.CanonicalFragmentRef) so they compare exactly against pipeline
// names, which always carry canonical bundle identities: a qualified
// exclusion drops only that bundle's fragment, leaving same-named fragments
// from other bundles intact. Every exclusion seam must build its set here
// and match via IsExcludedFragment.
func NewExclusionSet(exclusions []string) collections.Set[string] {
	set := collections.NewSet[string]()
	for _, e := range exclusions {
		// An exclusion whose bundle part will not canonicalize is added as
		// AUTHORED rather than dropped. Dropping it would silently widen the
		// context: an exclusion that fails to parse must still exclude
		// something, and its own text is the only honest candidate.
		canonical, err := remote.CanonicalFragmentRef(e)
		if err != nil {
			canonical = e
		}
		set.Add(canonical)
	}
	return set
}

// IsExcludedFragment reports whether a fragment name is excluded by a set
// built with NewExclusionSet. The name's bundle part is canonicalized before
// comparison, so any reference spelling of the same bundle matches; a name
// matches on its canonical qualified form (qualified exclusions) or on its
// bare fragment name (bare exclusions).
func IsExcludedFragment(name string, excluded collections.Set[string]) bool {
	canonical, err := remote.CanonicalFragmentRef(name)
	if err != nil {
		// Matched on the authored spelling, the same fallback NewExclusionSet
		// makes, so an unparseable ref on either side still meets the other.
		canonical = name
	}
	if excluded.Has(canonical) {
		return true
	}
	if bare, ok := remote.FragmentName(canonical); ok {
		return excluded.Has(bare)
	}
	return false
}
