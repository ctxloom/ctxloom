package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// testOutputDir is harp's recorded output dir, recording a fresh temp one
// (and a minimal sidecar, when the fixture made none) if the session has
// none — the state a mint leaves.
func testOutputDir(t *testing.T, harp string) string {
	t.Helper()
	out, err := sessions.OutputDir(harp)
	if errors.Is(err, sessions.ErrNoOutputDir) || errors.Is(err, sessions.ErrNotFound) {
		sidecar, perr := paths.HarpSidecarPath(harp)
		require.NoError(t, perr)
		if _, serr := os.Stat(sidecar); os.IsNotExist(serr) {
			require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
			require.NoError(t, os.WriteFile(sidecar, []byte("schema_version: 1\nproject_dir: /tmp/project\n"), 0o600))
		}
		m, oerr := sessions.Open(nil)
		require.NoError(t, oerr)
		out, err = m.RecordOutputDir(harp, t.TempDir())
	}
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(out, 0o755))
	return out
}

// harpEssencePath is harp's current essence path in its output dir.
func harpEssencePath(t *testing.T, harp string) (string, error) {
	t.Helper()
	return filepath.Join(testOutputDir(t, harp), paths.EssenceFileName), nil
}
