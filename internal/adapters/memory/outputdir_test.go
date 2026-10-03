package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// recordOutputDir gives harp a session record whose output dir is a fresh
// temp dir, the way a mint records one, and returns that dir.
func recordOutputDir(t *testing.T, harp string) string {
	t.Helper()
	out := t.TempDir()
	sidecar, err := paths.HarpSidecarPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	require.NoError(t, os.WriteFile(sidecar, []byte("project_dir: /proj\noutput_dir: "+out+"\n"), 0o600))
	return out
}
