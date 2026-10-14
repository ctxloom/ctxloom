package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The session index records how each of its runs ENDED — cause, detail, and
// the runner's exit status when one is known — so a session whose runner was
// killed (a signal, an OOM kill) does not read, afterwards, exactly like one
// that finished. ended_at alone said only WHEN.

func killedRunEnd(runID string) RunEnd {
	code := 137
	return RunEnd{
		RunID:    runID,
		At:       time.Unix(1_800_000_000, 0).UTC(),
		Cause:    "runner-loss",
		Detail:   "RunnerChannel disconnected; runner process: signal: killed",
		ExitCode: &code,
		Failed:   true,
	}
}

func TestManager_RecordRunEnd_PersistsTheEndToTheSidecar(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	require.NoError(t, m.RecordRunEnd(e.HarpName, killedRunEnd("run-1")))

	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.Len(t, found.RunEnds, 1)
	got := found.RunEnds[0]
	assert.Equal(t, "run-1", got.RunID)
	assert.Equal(t, "runner-loss", got.Cause)
	assert.Contains(t, got.Detail, "signal: killed")
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 137, *got.ExitCode)
	assert.True(t, got.Failed)

	// On disk, in the sidecar a human reads.
	raw, err := os.ReadFile(filepath.Join(m.root, e.HarpName, "session.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "run_ends:")
	assert.Contains(t, string(raw), "exit_code: 137")
	assert.Contains(t, string(raw), "cause: runner-loss")
}

func TestManager_RecordRunEnd_KeepsEveryRunsEnd(t *testing.T) {
	// A resume that fails to launch must not erase the end of the run it was
	// resuming: the verprop child's runner-loss was overwritten, in effect, by
	// its auto-resume's launch failure eight seconds later.
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.RecordRunEnd(e.HarpName, killedRunEnd("run-1")))
	require.NoError(t, m.RecordRunEnd(e.HarpName, RunEnd{RunID: "run-2", At: time.Now(), Cause: "launch-failed", Failed: true}))

	found, _ := m.Find(e.HarpName)
	require.Len(t, found.RunEnds, 2)
	assert.Equal(t, "run-1", found.RunEnds[0].RunID)
	assert.Equal(t, "run-2", found.RunEnds[1].RunID)
}

func TestManager_RecordRunEnd_SameRunReplacesItsEnd(t *testing.T) {
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.RecordRunEnd(e.HarpName, RunEnd{RunID: "run-1", Cause: "runner-loss"}))
	require.NoError(t, m.RecordRunEnd(e.HarpName, killedRunEnd("run-1")))

	found, _ := m.Find(e.HarpName)
	require.Len(t, found.RunEnds, 1, "one run has one end")
	require.NotNil(t, found.RunEnds[0].ExitCode)
}

func TestManager_RecordRunEnd_KeepsTheNewestBounded(t *testing.T) {
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	for i := range MaxRunEnds + 3 {
		require.NoError(t, m.RecordRunEnd(e.HarpName, RunEnd{RunID: fmt.Sprintf("run-%d", i), Cause: "final-reported"}))
	}
	found, _ := m.Find(e.HarpName)
	require.Len(t, found.RunEnds, MaxRunEnds)
	assert.Equal(t, "run-3", found.RunEnds[0].RunID, "the oldest ends are the ones dropped")
	assert.Equal(t, fmt.Sprintf("run-%d", MaxRunEnds+2), found.RunEnds[MaxRunEnds-1].RunID)
}

func TestMemStore_RecordRunEnd_MatchesTheManager(t *testing.T) {
	s := NewMemStore()
	e, err := s.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, s.RecordRunEnd(e.HarpName, RunEnd{RunID: "run-1", Cause: "runner-loss"}))
	require.NoError(t, s.RecordRunEnd(e.HarpName, killedRunEnd("run-1")))
	for i := range MaxRunEnds {
		require.NoError(t, s.RecordRunEnd(e.HarpName, RunEnd{RunID: fmt.Sprintf("later-%d", i)}))
	}
	found, err := s.Find(e.HarpName)
	require.NoError(t, err)
	require.Len(t, found.RunEnds, MaxRunEnds)
	assert.Equal(t, "later-0", found.RunEnds[0].RunID)

	assert.Error(t, s.RecordRunEnd("no-such-harp", RunEnd{RunID: "r"}))
}

// TestRecordRunEnd_StoresTheEndTimeInUTC pins that a run end's time is kept in
// UTC whatever zone the caller's clock reported it in, in the Manager's
// sidecar and the MemStore alike, so session.yaml never mixes offsets.
func TestRecordRunEnd_StoresTheEndTimeInUTC(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 30, 0, 0, time.FixedZone("UTC+5", 5*3600))
	end := RunEnd{RunID: "run-1", At: at, Cause: "final-reported"}

	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.RecordRunEnd(e.HarpName, end))
	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.Len(t, found.RunEnds, 1)
	assert.True(t, found.RunEnds[0].At.Equal(at), "the same instant")
	assert.Equal(t, time.UTC, found.RunEnds[0].At.Location(), "stored in UTC")

	s := NewMemStore()
	me, err := s.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, s.RecordRunEnd(me.HarpName, end))
	mfound, err := s.Find(me.HarpName)
	require.NoError(t, err)
	require.Len(t, mfound.RunEnds, 1)
	assert.Equal(t, time.UTC, mfound.RunEnds[0].At.Location(), "the MemStore matches the Manager")
}
