package coordgrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// An ended run's terminal cause and detail reach the wire roster, so a remote
// roster reader can tell a failed child from a finished one.
func TestRunsSnapshotToWire_CarriesTheTerminalCause(t *testing.T) {
	got := RunsSnapshotToWire(coord.RunsSnapshot{Runs: []coord.RunInfo{
		{RunID: "failed", Phase: coord.StateEnded, Cause: coord.CauseLaunchFailed, Detail: "engine binary not found"},
		{RunID: "live", Phase: coord.StateExecuting},
	}}).GetRuns()
	require.Len(t, got, 2)
	assert.Equal(t, coord.CauseLaunchFailed, got[0].GetCause())
	assert.Equal(t, "engine binary not found", got[0].GetDetail())
	assert.Empty(t, got[1].GetCause())
	assert.Empty(t, got[1].GetDetail())
}
