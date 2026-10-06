package operations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// mustParseProducerRef parses ref — an item ref a producer emitted
// (ItemRead.TrustRef, LoadedSkill.TrustRef, or a ref built by hand through
// trust.BundleRef.WithItem in a test) — into the trust.Ref shape a test asserts
// IsLocal/RepoURL/Key() against. A producer emits the canonical
// "ctxloom+<class>:...#<kind>/<item>" grammar, so a test asserting against a
// producer's literal output must parse it in that grammar.
func mustParseProducerRef(t *testing.T, ref string) trust.Ref {
	t.Helper()
	br, err := trust.ParseBundleRef(ref)
	require.NoError(t, err, "ref %q must parse as the canonical bundle-reference grammar", ref)
	return trust.RefFromBundleRef(br)
}
