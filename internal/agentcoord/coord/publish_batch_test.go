package coord

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// newPublishCoordinator is newTestCoordinator with the state dir handed back,
// so a test can read the items journal FILE — the only place an intra-batch
// duplicate is observable (the fold's own (run, seq) guard hides it from every
// counts-based assertion).
func newPublishCoordinator(t *testing.T) (*Coordinator, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(Options{ProjectDir: dir, StateDir: dir, Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return c, dir
}

// itemFactLines counts the journaled item facts mentioning runID.
func itemFactLines(t *testing.T, stateDir, runID string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "items.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read items journal: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, runID) {
			n++
		}
	}
	return n
}

// The (run_id, seq) dedupe inside PublishEvents' Exec closure read
// c.itemsF.maxSeq, which does not advance until the append COMMITS — so two
// events carrying the same (run_id, seq) in ONE batch both passed the check and
// both were journaled. The fold's own guard hides the double from every
// counts-based assertion, but the journal grows by a duplicate record every
// time, which is exactly what the dedupe's own comment says must not happen
// ("a retried publish must not grow the journal unboundedly") — and an
// at-least-once publisher retrying a batch it half-sent is the ordinary case
// that produces one.
func TestPublishEvents_DedupesWithinASingleBatch(t *testing.T) {
	c, dir := newPublishCoordinator(t)

	const runID = "run-intrabatch"
	ev := runCompletedEvent(runID, 1, "ok")
	resp := c.PublishEvents([]*agentcoordpb.AgentEvent{ev, ev, ev})

	assert.Empty(t, resp.GetRejected())
	assert.Equal(t, uint64(1), resp.GetCommittedSeqByRun()[runID])
	assert.Equal(t, 1, itemFactLines(t, dir, runID),
		"three copies of one (run_id, seq) in a single batch must journal ONE fact — "+
			"the in-batch duplicates were appended because the watermark only advances at commit")

	c.items.View(func() {
		assert.Equal(t, 1, c.itemsF.countsFor(runID)["run_completed"])
	})
}

// TestPublishEvents_DistinctSeqsInOneBatchAllLand keeps the intra-batch guard
// from swallowing a legitimate multi-event batch (the backfill case).
func TestPublishEvents_DistinctSeqsInOneBatchAllLand(t *testing.T) {
	c, dir := newPublishCoordinator(t)

	const runID = "run-batch-ok"
	resp := c.PublishEvents([]*agentcoordpb.AgentEvent{
		runCompletedEvent(runID, 1, "one"),
		runCompletedEvent(runID, 2, "two"),
		runCompletedEvent(runID, 3, "three"),
	})
	assert.Empty(t, resp.GetRejected())
	assert.Equal(t, uint64(3), resp.GetCommittedSeqByRun()[runID])
	assert.Equal(t, 3, itemFactLines(t, dir, runID), "three distinct seqs journal three facts")
}

// An EMPTY batch returned a success response with an empty
// CommittedSeqByRun and no Rejected entries — byte-identical to "everything you
// sent is committed", which for a publisher whose event assembly silently
// produced nothing is this project's characteristic failure: a green signal
// carrying zero payload. The gRPC entry point passed an empty request straight
// through.
//
// The in-process core cannot signal it (it returns only a response, and the
// response has no field that means "you sent nothing"), so it says so on the
// diagnostic channel.
func TestPublishEvents_EmptyBatchIsNotSilent(t *testing.T) {
	c, _ := newPublishCoordinator(t)

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resp := c.PublishEvents(nil)

	assert.Empty(t, resp.GetRejected())
	assert.Empty(t, resp.GetCommittedSeqByRun())
	assert.Contains(t, buf.String(), "no events",
		"an empty publish must be announced: the response cannot distinguish it from a full commit")
}

// TestPublishEventsGRPC_RejectsAnEmptyRequest is F22's wire half: a real
// publisher sending an empty batch gets a typed refusal instead of a success it
// can only misread.
func TestPublishEventsGRPC_RejectsAnEmptyRequest(t *testing.T) {
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, func() *fakeEngine { return &fakeEngine{oneshot: true} })
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "hello", "", "")
	require.NoError(t, err)
	h := childHome(t, c, out.RunID)

	client := agentcoordpb.NewCoordinatorServiceClient(h.conn)
	_, err = client.PublishEvents(context.Background(), &agentcoordpb.PublishEventsRequest{})
	require.Error(t, err, "an empty batch must not answer with a success response")
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "no events")
}
