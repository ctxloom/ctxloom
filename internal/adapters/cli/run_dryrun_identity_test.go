package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
)

// A dry run previews; it does not establish the project. In a directory with
// no project yet it leaves no in-tree marker and no registry entry — the first
// real `ctxloom run` (a write) mints them.
func TestRunDryRun_DoesNotMintTheProjectIdentity(t *testing.T) {
	dir := runCLIFixture(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "hi")
	require.NoError(t, res.err, "stderr:\n%s", res.stderr)

	marker, err := projectid.ReadMarker(dir)
	require.NoError(t, err)
	require.Empty(t, marker, "a dry run must not mint the project marker")
	require.NoDirExists(t, filepath.Join(home, ".ctxloom", "projects"), "a dry run must not create the project registry")
}
