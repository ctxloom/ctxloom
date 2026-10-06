//go:build acceptance

package acceptance

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// canonicalBundleRef mints the canonical URI addressing a bundle published at
// repoURL, through the same Ref -> BundleRef bridge every reader stamps its
// own source with. A scenario that composed the URI by hand would be asserting
// against a spelling nothing produces.
//
// It PANICS on an unaddressable pair: a fixture that cannot name the bundle it
// just published has no scenario left to run, and a returned error here would
// be reported as the step's own failure rather than as the fixture's.
func canonicalBundleRef(repoURL, bundle string) string {
	br, err := trust.Ref{RepoURL: repoURL, Bundle: bundle}.AsBundleRef()
	if err != nil {
		panic(fmt.Sprintf("acceptance fixture: bundle %q at %q is not addressable: %v", bundle, repoURL, err))
	}
	return br.String()
}
