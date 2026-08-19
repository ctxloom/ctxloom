package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// ResolveItemAsk is the door a trust ref comes through from argv
// (`ctxloom bundle trust <ref>`), from an MCP argument, and from the gates
// built over bundle-authored names. Whatever it yields is interpolated
// verbatim into the countersign preimage by CountersignRef, so a control
// character surviving here reaches the bytes a human's signature covers —
// where an LF closes the `ref:` line early and forges the rest of the frame.
//
// The property asserted is the one that matters: whatever the ask does, the
// address a countersignature would bind to carries no control character. Both
// halves of the ref are dirtied, because a bundle half and an item half travel
// different arms and only one of them is a name the catalog could match.
func TestResolveItemAsk_ControlCharactersNeverReachTheCountersignPreimage(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{"mybundle": {Version: "1.0.0"}}).Catalog()

	for _, tc := range []struct {
		name, ref string
	}{
		{"forged header tail in the bundle half", "mybundle\nform: fragment/raw\nlen: 15\n#fragments/a"},
		{"forged header tail in the item half", "mybundle#fragments/a\nform: fragment/raw"},
		{"control character inside a canonical URI", "ctxloom+local:mybundle\n#fragments/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			br, err := ResolveItemAsk(cat, tc.ref)
			if err != nil {
				return // refused outright: nothing reaches the preimage at all
			}
			refStr := CountersignRef(trust.RefFromBundleRef(br))
			assert.NotContains(t, refStr, "\n",
				"a ref that resolved must be addressable without carrying a preimage-forging newline")
			h := signing.CountersignHeader{
				Assertion: signing.AssertionApprove,
				Ref:       refStr,
				Form:      signing.AttestFragmentRaw,
			}
			assert.NoError(t, h.Validate(),
				"a ref that came through the ask boundary must never be refused by the preimage guard")
		})
	}
}

// TestResolveItemAsk_CleanRefsResolve pins the other half: every legal spelling
// of an item this catalog holds resolves, and resolves to an addressable
// identity.
func TestResolveItemAsk_CleanRefsResolve(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{
		"mybundle": {Version: "1.0.0"},
		"lang/go":  {Version: "1.0.0"},
	}).Catalog()

	for _, ref := range []string{
		"mybundle#fragments/a",
		"lang/go#commands/review",
		"ctxloom+local:mybundle#mcp/postgres",
		"ctxloom+git://github.com/acme/repo//bundles/core#mcp/postgres",
		"ctxloom+builtin:ltk#fragments/x",
		"ctxloom+companion:ltk#hooks/pre_tool/0",
	} {
		br, err := ResolveItemAsk(cat, ref)
		require.NoError(t, err, ref)
		assert.NotEmpty(t, br.Item, ref)
		assert.NotEmpty(t, CountersignRef(trust.RefFromBundleRef(br)), ref)
	}
}
