package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// trust.ParseItemRef is the door a trust ref comes through from argv
// (`ctxloom trust <ref>`), from an MCP argument, and from the gates built over
// bundle-authored names. Whatever it yields is interpolated verbatim into the
// countersign preimage by countersignRef, so a control character surviving here
// reaches the bytes a human's signature covers — where an LF closes the `ref:`
// line early and forges the rest of the frame.
//
// The bare-local fallback is what makes this reachable rather than theoretical:
// a token carrying no scheme marker is accepted as a plain local bundle name
// with no grammar check at all.
func TestParseTrustItemRef_StripsControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name, ref, wantBundle, wantName string
	}{
		{
			name:       "bare local name with a forged header tail",
			ref:        "mybundle\nform: fragment/raw\nlen: 15\n#fragments/a",
			wantBundle: "mybundleform: fragment/raw" + "len: 15",
			wantName:   "a",
		},
		{
			name:       "control character in the item name",
			ref:        "mybundle#fragments/a\nform: fragment/raw",
			wantBundle: "mybundle",
			wantName:   "aform: fragment/raw",
		},
		{
			name:       "canonical ref",
			ref:        "https://github.com/acme/repo@bundles/core\n#fragments/a",
			wantBundle: "core",
			wantName:   "a",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tRef, loadRef, _, err := trust.ParseItemRef(tc.ref)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBundle, tRef.Bundle)
			assert.Equal(t, tc.wantName, tRef.Name)
			assert.NotContains(t, loadRef, "\n")

			// The whole point: the countersign preimage's ref field is clean,
			// so the header the store would sign is one the closed vocabulary
			// accepts.
			refStr := countersignRef(tRef)
			assert.NotContains(t, refStr, "\n")
			h := signing.CountersignHeader{
				Assertion: signing.AssertionApprove,
				Ref:       refStr,
				Form:      signing.AttestFragmentRaw,
			}
			assert.NoError(t, h.Validate(),
				"a ref that came through ingest must never be refused by the preimage guard")
		})
	}
}

// TestParseTrustItemRef_CleanRefsAreUnchanged pins the other half: ingest is a
// no-op for every legal ref shape.
func TestParseTrustItemRef_CleanRefsAreUnchanged(t *testing.T) {
	for _, ref := range []string{
		"mybundle#fragments/a",
		"lang/go#commands/review",
		"https://github.com/acme/repo@bundles/core#mcp/postgres",
		"ctxloom:local@bundles/dev#hooks/pre",
		"builtin:ltk#fragments/x",
	} {
		tRef, loadRef, _, err := trust.ParseItemRef(ref)
		require.NoError(t, err, ref)
		assert.NotEmpty(t, tRef.Name, ref)
		assert.NotEmpty(t, loadRef, ref)
	}
}
