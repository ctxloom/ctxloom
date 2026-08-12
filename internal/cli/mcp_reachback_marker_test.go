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
// invariant for the off-Linux reach-back marker. The value is written by
// internal/acp's container transport and read by internal/cli's forward shim,
// and those two packages sit on opposite sides of the one-door layering
// boundary — neither may import the other (internal/acptest's no-import test).
// The ONLY admissible home for the contract is therefore the zero-import leaf
// internal/shared/mcpsocket, and both ends must read it from there.
//
// A prior review claimed internal/cli holds its own `reachBackTCPPrefix`
// constant kept in sync with internal/acp by a comment asking humans to do
// it. That is no longer true — both ends read mcpsocket.TCPPrefix. This test
// is what keeps it untrue: it goes red the moment either package grows its
// own literal copy of the marker, which is precisely the state described.
func TestReachBackMarker_HasExactlyOneDeclaration(t *testing.T) {
	require.Equal(t, "tcp://", mcpsocket.TCPPrefix, "the marker's value is the contract both ends encode/decode")

	// Production sources on both sides of the boundary. Comments are allowed
	// to quote the marker (they explain the wire form); only a Go string
	// literal in code is a second declaration.
	// Absolute, from the compiled-in source paths — not "." / "../acp" (see
	// pkgSourceDir): TestMain sandboxes the binary into a temp cwd, where
	// neither relative path resolves.
	// internal/mcp is in the list because the forward shim that DECODES the
	// marker moved there with the rest of the MCP implementation; internal/cli
	// stays because the invariant is "no package grows its own literal", and
	// this package is where a fresh copy would most plausibly reappear.
	for _, dir := range []string{
		pkgSourceDir(t),
		filepath.Join(repoDir(t), "internal", "mcp"),
		filepath.Join(repoDir(t), "internal", "acp"),
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
