package trust

import "fmt"

// ItemRef mints the canonical "<source>#<kind>/<item>" reference an item's
// trust key is built from, given the bundle-level ref its reader already
// stamped.
//
// It returns the fallback string ALONGSIDE the error rather than instead of it.
// The caller needs both: the string, because an item must key somewhere stable
// even when it cannot be addressed, and the error, because an unaddressable
// item is WITHHELD from delivery and a caller that cannot see the failure
// reports content vanishing as success. Every caller is expected to report the
// error; none may treat the returned string as a successful mint.
//
// The fallback carries kind and item, not src alone. WithItem fails BEFORE they
// land on the BundleRef, so a %#v of src is identical for every item of one
// unaddressable bundle — without them, a fragment and a command sharing a
// source degrade to the SAME key, only one is ever tallied, and the other's
// withholding looks like it never happened. It also cannot be confused with a
// real Identity(), which never starts with the literal scheme prefix followed
// by "unaddressable:".
func ItemRef(src BundleRef, kind ItemKind, item string) (string, error) {
	br, err := src.WithItem(kind, item)
	if err != nil {
		return fmt.Sprintf("ctxloom+unaddressable:%#v#%s/%s", src, kind.Dir(), item), err
	}
	return br.String(), nil
}

// ItemRefFromSource is ItemRef for a caller holding the SOURCE STRING rather
// than a stamped BundleRef — a builtin or companion read's own source ref, or
// the literal bundle-ref ask a profile resolved.
//
// It replays ParseItemRef's base-ref resolution by handing it a throwaway
// selector and discarding the parsed Kind/Name, rather than re-implementing
// those rules a second, competing way — the hazard AsBundleRef's own doc warns
// about for a fresh conversion path.
//
// This lived three times over — internal/config, internal/lm/backends and, in
// the typed form above, internal/bundles — and the copies drifted in exactly
// the way that matters: when a grammar change made one class of source
// unconvertible, each copy degraded silently on its own terms and the failure
// had to be diagnosed three times. One implementation, one fallback spelling.
func ItemRefFromSource(source string, kind ItemKind, item string) (string, error) {
	unaddressable := fmt.Sprintf("ctxloom+unaddressable:%#v#%s/%s", source, kind.Dir(), item)
	base, err := BundleRefFromSource(source)
	if err != nil {
		return unaddressable, err
	}
	br, err := base.WithItem(kind, item)
	if err != nil {
		return unaddressable, err
	}
	return br.String(), nil
}

// BundleRefFromSource resolves a source string — the honest bundle identity
// every executable-surface producer receives — into a bundle-level BundleRef.
//
// It never guesses: a source it cannot resolve is an error, not a zero value
// quietly returned. Returning the zero BundleRef alone is what let a refused
// spelling travel three layers before surfacing as withheld content.
func BundleRefFromSource(source string) (BundleRef, error) {
	tRef, _, _, err := ParseItemRef(source + "#" + KindFragment.Dir() + "/x")
	if err != nil {
		return BundleRef{}, fmt.Errorf("parse %q: %w", source, err)
	}
	tRef.Kind, tRef.Name = "", ""
	br, err := tRef.AsBundleRef()
	if err != nil {
		return BundleRef{}, fmt.Errorf("convert %q: %w", source, err)
	}
	return br, nil
}
