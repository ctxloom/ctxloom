package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestRunnerDepsFor_TightensALooseRecordDirBeforeAnyDelivery: a session's
// delivery runs claude's MCP approach through fsstatic's copy-on-write
// overlay, which cannot chmod the existing records directory the approach
// writes its undo record into. The runner's ownership record is opened here,
// when its ports are composed and so before any delivery, and opening it on
// the real filesystem is what tightens a directory an older binary left
// readable to everyone.
func TestRunnerDepsFor_TightensALooseRecordDirBeforeAnyDelivery(t *testing.T) {
	recordsDir := filepath.Join(t.TempDir(), "records")
	t.Cleanup(paths.SetHomeRecordsDirForTesting(recordsDir))
	require.NoError(t, os.MkdirAll(recordsDir, 0o755))
	require.NoError(t, os.Chmod(recordsDir, 0o755))

	deps, err := runnerDepsFor(nil, "claude-code", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, deps.Records)

	info, err := os.Stat(recordsDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
