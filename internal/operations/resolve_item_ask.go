package operations

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// ResolveItemAsk resolves what a user typed at `ctxloom bundle
// trust|reject|forget` into the canonical item reference the countersign
// store keys on.
//
// A canonical URI ("ctxloom+<class>:…#<kind>/<item>") resolves WITHOUT
// consulting the catalog: trust.ParseBundleRef alone decides whether it
// parses, and its result is returned as-is. This is deliberate and it is the
// property SetBlacklist's "written even when the item cannot be resolved"
// rests on — an item must still be REJECTABLE after the bundle that carried
// it is gone (uninstalled, retracted, never pulled on this machine), and a
// catalog membership check here would make that impossible.
//
// Anything else is read as "<bare-bundle-ask>#<kind>/<item>": the bundle half
// resolves against cat (bundles.Catalog.ResolveAsk — a bare name, a retired
// spelling refused, or an ambiguous name refused), and the item's kind/name
// selector is attached to whatever that resolves to.
func ResolveItemAsk(cat bundles.Catalog, ask string) (trust.BundleRef, error) {
	if br, err := trust.ParseBundleRef(ask); err == nil {
		return br, nil
	}

	base, sel, found := strings.Cut(ask, "#")
	if !found || base == "" {
		return trust.BundleRef{}, fmt.Errorf("item ref %q missing #<kind>/<name> selector", ask)
	}
	kind, name, err := trust.ParseSelector(sel)
	if err != nil {
		return trust.BundleRef{}, fmt.Errorf("invalid item ref %q: %w", ask, err)
	}

	br, err := cat.ResolveAsk(base)
	if err != nil {
		return trust.BundleRef{}, err
	}
	br.Kind = kind
	br.Item = name
	return br, nil
}

// resolveMutationTarget is the shared preamble of the three trust mutations
// (`bundle trust`, `bundle reject`, `bundle forget`): the resolved set they
// read content through, the trust identity the countersign record keys on, and
// the bundle key that content loads from.
//
// The set comes BACK rather than being resolved again by the caller, because
// the ask was answered against it — a second resolve could see a different set
// than the one that decided which item the ask names.
//
// With no loader and no config there is nothing to resolve against, and the
// EMPTY set is the honest answer: a mutation whose ask resolves to nothing
// refuses, which is the same verdict as an ask for a bundle that is not there
// and safer than either.
func resolveMutationTarget(cfg *config.Config, reqLoader *bundles.Loader, ask string) (bundles.Catalog, trust.Ref, trust.BundleKey, error) {
	loader := reqLoader
	if loader == nil && cfg != nil {
		loader = bundleLoader(cfg)
	}
	var cat bundles.Catalog
	if loader != nil {
		cat = loader.Catalog()
	}
	br, err := ResolveItemAsk(cat, ask)
	if err != nil {
		return bundles.Catalog{}, trust.Ref{}, "", err
	}
	return cat, trust.RefFromBundleRef(br), br.BundleIdentity(), nil
}
