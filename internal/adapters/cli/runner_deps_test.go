package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestRunnerDepsFor_TightensALooseRecordDirBeforeAnyDelivery: the runner's
// ownership record is opened when its ports are composed, and opening an
// existing records directory on the real filesystem tightens one an older
// binary left readable to everyone. A delivery does not rely on this: it
// prepares the record itself (delivery.Ownership.Prepare).
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
