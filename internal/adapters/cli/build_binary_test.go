package cli

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// buildCtxloomBinary compiles the ctxloom CLI to a tempdir and returns the
// path. We rebuild per-test-run rather than relying on the user's $PATH so
// the integration test exercises the source under test, not a stale install.
func buildCtxloomBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wire-protocol integration test in -short mode")
	}

	moduleRoot, err := sourcedir.RepoRoot()
	require.NoError(t, err, "locate the module root to build the CLI from")

	binDir := t.TempDir()
	binName := "ctxloom"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	// The ctxloom main package now lives at ./cmd/ctxloom (was the repo root).
	cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags", testsupport.TestBinaryLDFlags, "-o", binPath, "./cmd/ctxloom")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return binPath
}
