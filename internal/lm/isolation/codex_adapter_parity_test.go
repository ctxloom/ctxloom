//go:build parked_engines

// PARKED (parked_engines): this whole file pins codex's ACP-adapter literal
// against internal/codex's own descriptor, and internal/codex is out of the
// default build. grep -rn parked_engines finds every parked site.
package isolation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestCodexInstallFragment_MatchesTheACPTransportDeclaration pins that
// the adapter binary name "codex-acp" and its npm package are written
// twice — once in this package's image-install fragment, once in
// internal/codex's ACP-transport descriptor — and the fragment's own comment
// says to "keep this binary name in sync BY HAND". A comment is not a
// mechanism: the two literals stay equal right up until one moves, and the
// failure is silent at image-build time and only appears as a LookPath error
// inside a containerized codex agent's structured chat, which is exactly the
// gap the fragment was extended to close in the first place.
//
// Sourcing the fragment FROM the descriptor is the real fix and is a
// separate, more invasive slice: this package deliberately does not import
// internal/lm/backends (engineContainerSpec's doc — it would drag the whole
// backend tree into the isolation seam), and internal/codex depends on this
// package, so neither literal can move to the other without a layering
// change. What CAN be had now is the invariant, maintained rather than
// asserted in prose: an external test package may import internal/codex
// where production code may not, so the drift the comment asks a human to
// prevent turns this red instead.
func TestCodexInstallFragment_MatchesTheACPTransportDeclaration(t *testing.T) {
	// Membership on the tokenised fragment, never a substring search: a
	// renamed adapter ("codex-acp-next") CONTAINS the old name, so Contains
	// stays green through exactly the drift this test exists to catch. That
	// was measured on the first draft of this pin.
	fragmentTokens := map[string]bool{}
	for _, tok := range strings.Fields(string(isolation.CodexInstallFragmentForTest)) {
		fragmentTokens[tok] = true
	}
	require.NotEmpty(t, fragmentTokens)

	assert.True(t, fragmentTokens[codex.CodexACPAdapter],
		"the image must install and gate the SAME adapter binary internal/codex's Chat() looks for on PATH (%q)", codex.CodexACPAdapter)

	installPkg := ""
	for _, tok := range strings.Fields(codex.CodexACPTransport.InstallCmd) {
		if strings.HasPrefix(tok, "@") {
			installPkg = tok
		}
	}
	require.NotEmpty(t, installPkg, "the descriptor's InstallCmd must name a scoped npm package: %q", codex.CodexACPTransport.InstallCmd)
	assert.True(t, fragmentTokens[installPkg],
		"the image must install the SAME npm package codex.CodexACPTransport.InstallCmd names (%q)", installPkg)

	assert.Equal(t, codex.CodexACPAdapter, codex.CodexACPTransport.Binary,
		"the descriptor's binary is the adapter constant, so the fragment check above covers the descriptor too")
}
