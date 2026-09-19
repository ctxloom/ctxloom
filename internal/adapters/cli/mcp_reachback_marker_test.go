package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/mcpsocket"
)

// TestReachBackMarker_HasExactlyOneDeclaration pins the anti-duplication
// invariant for the off-Linux reach-back marker: the marker has exactly ONE
// declaration, mcpsocket.TCPPrefix, and every package that encodes or decodes
// it reads that constant rather than spelling the literal again.
//
// internal/shared/mcpsocket is a zero-import leaf precisely so that any
// package on either side of the one-door layering boundary can import it
// without importing the other. A hand-kept second copy is the failure this
// guards: an earlier revision did hold a private `reachBackTCPPrefix` in
// internal/adapters/cli, synchronised only by a comment asking humans to do it.
// This test goes red the moment any scanned package grows its own literal.
func TestReachBackMarker_HasExactlyOneDeclaration(t *testing.T) {
	require.Equal(t, "tcp://", mcpsocket.TCPPrefix, "the marker's value is the contract both ends encode/decode")

	// Production sources on both sides of the boundary. Comments are allowed
	// to quote the marker (they explain the wire form); only a Go string
	// literal in code is a second declaration.
	// Absolute, from the compiled-in source paths — not "." / a relative
	// sibling (see pkgSourceDir): TestMain sandboxes the binary into a temp
	// cwd, where no relative path resolves.
	// internal/adapters/mcp is in the list because it holds the forward shim that
	// DECODES the marker — mcp_forward.go's mcpsocket.TCPPrefix CutPrefix is
	// the live consumer this scan protects. internal/adapters/cli stays because the
	// invariant is "no package grows its own literal", and this package is
	// where a fresh copy would most plausibly reappear: the shim used to live
	// here, and the private copy the doc above describes was here.
	for _, dir := range []string{
		pkgSourceDir(t),
		filepath.Join(repoDir(t), "internal", "adapters", "mcp"),
	} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err, "read %s", dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			raw, rerr := os.ReadFile(path)
			require.NoError(t, rerr, "read %s", path)
			for i, line := range strings.Split(string(raw), "\n") {
				// A whole-line comment is prose about the wire form and may
				// quote the marker. Anything else is code. (The marker itself
				// contains "//", so splitting a line on the comment token
				// would swallow the very literal being looked for.)
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
					continue
				}
				assert.NotContains(t, line, `"`+mcpsocket.TCPPrefix,
					"%s:%d declares its own copy of the reach-back marker — read mcpsocket.TCPPrefix instead (it is the one home both packages may import)", path, i+1)
			}
		}
	}
}
