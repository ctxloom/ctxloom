package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// recordSummary/recordArtifact warn on a journal failure and carry
// straight on — and the two things they carried on to both assert something
// that did not happen:
//
//   - c.audit("agent_report", ...) files an interaction saying the report was
//     received, so interactions.jsonl records a report the reports journal does
//     not contain. That log is the operator's record of what the coordination
//     did; stderr, where the warning went, is a channel that is redirected away
//     entirely under `ctxloom run` (clidiag.SetSink).
//   - maybeCheckpointOnSummary writes an items snapshot, whose own doc says it
//     is called "AFTER the summary fact itself is durably journaled (the
//     checkpoint's own record must exist before the snapshot that references it
//     as the compaction point)". On this path that record does not exist.
//
// A closed journal is the deterministic shape of the failure (and a real one: a
// coordinator shutting down while a runner's last report is in flight).
func TestRecordSummary_JournalFailureDoesNotClaimTheReportWasFiled(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Options{ProjectDir: dir, StateDir: dir, Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	// Break the append path the way a shutdown does.
	if !assert.NoError(t, c.runs.Close()) {
		return
	}

	c.recordSummary("child-a", "run-1", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_CHECKPOINT,
		Text:  "everything up to seq 40",
	})

	assert.Empty(t, c.LatestReport("child-a"), "precondition: nothing was journaled")

	interactions, err := os.ReadFile(filepath.Join(dir, "interactions.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read interactions journal: %v", err)
	}
	assert.False(t, strings.Contains(string(interactions), "agent_report"),
		"a report that never reached the journal must not be audited as filed — that leaves the "+
			"interaction log asserting a durable record that does not exist")

	_, statErr := os.Stat(itemsSnapshotPath(dir))
	assert.True(t, os.IsNotExist(statErr),
		"no checkpoint snapshot may be written for a CHECKPOINT report that was never journaled: the "+
			"snapshot's own contract is that the report it compacts to exists first")
}

// TestRecordSummary_SuccessStillAuditsAndCheckpoints is the other half: the
// early return must not cost the ordinary path its audit entry or its
// checkpoint.
func TestRecordSummary_SuccessStillAuditsAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Options{ProjectDir: dir, StateDir: dir, Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	c.recordSummary("child-a", "run-1", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_CHECKPOINT,
		Text:  "everything up to seq 40",
	})

	if !assert.Contains(t, c.LatestReport("child-a"), "everything up to seq 40") {
		return
	}
	interactions, err := os.ReadFile(filepath.Join(dir, "interactions.jsonl"))
	if !assert.NoError(t, err) {
		return
	}
	assert.Contains(t, string(interactions), "agent_report", "a filed report is audited")
	_, statErr := os.Stat(itemsSnapshotPath(dir))
	assert.NoError(t, statErr, "a CHECKPOINT report still triggers the compaction snapshot")
}
