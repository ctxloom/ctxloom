package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegularFileExists pins this package's "existing regular file"
// predicate (recoverTargetSessionID's production transcriptExists — the
// former cli fileExists was inlined into operations.SessionEssenceInfo and
// deleted, see internal/operations's own coverage of that check). Other
// packages carry their own verbatim copies — internal/lm/isolation
// (fileExists) and internal/codex (codexFileExists) — deliberately NOT
// unified with this one (wave-brief §4 DUPLICATE, verbatim case), so each is
// pinned separately with the same three cases: the behaviour any future
// collapse must preserve is stated, and a divergence introduced in one of
// them shows up where it happens.
//
// Split out of internal/cli's session_cmd_test.go when the MCP
// implementation moved here: the predicate is production code of the memory
// tools, so its pin belongs with them.
func TestRegularFileExists(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "essence.md")
	require.NoError(t, os.WriteFile(regular, []byte("x"), 0o644))

	assert.True(t, regularFileExists(regular), "an existing regular file exists")
	assert.False(t, regularFileExists(dir), "a DIRECTORY is not a file — the whole point of the IsDir check")
	assert.False(t, regularFileExists(filepath.Join(dir, "absent.md")), "a missing path does not exist")
}
