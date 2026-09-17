package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestListAllSessions_StaleIndexYAMLIsNotASource pins, at the operations
// façade every frontend reads through, that the session listing is the set of
// session directories and nothing else: an index.yaml sitting at the root
// after the one-time migration contributes no rows.
func TestListAllSessions_StaleIndexYAMLIsNotASource(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open()
	require.NoError(t, err)
	real, err := mgr.AssignHarp("/proj/a", "claude-code")
	require.NoError(t, err)

	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, paths.MigratedIndexFileName), []byte("sessions: []\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, paths.IndexFileName), []byte(`sessions:
  - harp_name: ghost-row
    project_dir: /proj/a
    backend: claude-code
    started_at: 2026-08-01T10:00:00Z
`), 0o644))

	got, err := ListAllSessions()
	require.NoError(t, err)
	var names []string
	for _, e := range got {
		names = append(names, e.HarpName)
	}
	assert.Equal(t, []string{real.HarpName}, names)

	ghost, err := GetSession("ghost-row")
	require.NoError(t, err)
	assert.Nil(t, ghost)
}

// TestListAllSessions_PurgedDirectoryListsAsPurged: a purge leaves the
// directory and removes its content; the listing must report it as purged —
// never drop it as damage. There is no reconcile pass any more to do the
// dropping, and this pins that nothing else took its place.
func TestListAllSessions_PurgedDirectoryListsAsPurged(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open()
	require.NoError(t, err)
	e, err := mgr.AssignHarp("/proj/a", "claude-code")
	require.NoError(t, err)
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o644))
	require.NoError(t, mgr.BindSession(e.HarpName, "sess-1", transcript))

	_, err = PurgeSession(e.HarpName, PurgeSessionRequest{Populations: []PurgePopulation{PurgePopulationTranscript}, Undistilled: true, Apply: true})
	require.NoError(t, err)
	require.NoError(t, os.Remove(transcript))

	got, err := ListAllSessions()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, e.HarpName, got[0].HarpName)
	assert.NotNil(t, got[0].PurgedAt)
}
