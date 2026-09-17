package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Wave C4 deliverable 1 acceptance: "oneshot backend publishes via
// PublishEvents with dedupe proven hermetically."

func runCompletedEvent(runID string, seq uint64, text string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		RunId: runID,
		Seq:   seq,
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_SUCCEEDED, Text: text},
		}},
	}
}

// TestPublishEvents_DedupesOnRunIDSeq pins the contract's core guarantee
// ("Safe to retry: dedupe on (run_id, seq)"): republishing the SAME event
// commits the same watermark and does not double-count in the durable items
// fold — proven hermetically against the Go-level API, no network hop.
func TestPublishEvents_DedupesOnRunIDSeq(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	ev := runCompletedEvent("run-dedupe-1", 1, "ok")

	resp1 := c.PublishEvents([]*agentcoordpb.AgentEvent{ev})
	assert.Empty(t, resp1.GetRejected())
	assert.Equal(t, uint64(1), resp1.GetCommittedSeqByRun()["run-dedupe-1"])

	// Retry: the SAME (run_id, seq) event, as an at-least-once republisher
	// would send after a flaky-network timeout that actually succeeded.
	resp2 := c.PublishEvents([]*agentcoordpb.AgentEvent{ev})
	assert.Empty(t, resp2.GetRejected())
	assert.Equal(t, uint64(1), resp2.GetCommittedSeqByRun()["run-dedupe-1"])

	c.items.View(func() {
		assert.Equal(t, 1, c.itemsF.countsFor("run-dedupe-1")["run_completed"],
			"a retried publish must not double-count in the durable fold")
	})
}

// TestPublishEvents_BackfillsGaps pins "may span gaps being backfilled":
// events for the same run_id arrive out of a single call's order and the
// watermark advances to the highest committed seq.
func TestPublishEvents_BackfillsGaps(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	resp := c.PublishEvents([]*agentcoordpb.AgentEvent{
		runCompletedEvent("run-gap-1", 1, "first"),
		runCompletedEvent("run-gap-1", 2, "second"),
	})
	assert.Empty(t, resp.GetRejected())
	assert.Equal(t, uint64(2), resp.GetCommittedSeqByRun()["run-gap-1"])

	c.items.View(func() {
		assert.Equal(t, 2, c.itemsF.countsFor("run-gap-1")["run_completed"])
	})
}

// TestPublishEvents_RejectsMalformed pins the reject vocabulary: a missing
// run_id/seq, and a payload kind this window does not carry over the unary
// fallback (Custom — it rides RunChannel's per-role seam, keyed by harp, not
// run_id).
func TestPublishEvents_RejectsMalformed(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	resp := c.PublishEvents([]*agentcoordpb.AgentEvent{
		{RunId: "", Seq: 1, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}},
		{RunId: "run-x", Seq: 0, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}},
		{RunId: "run-x", Seq: 1, Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "vendor/thing"}}},
	})
	require.Len(t, resp.GetRejected(), 3)
	assert.Equal(t, int32(codes.InvalidArgument), resp.GetRejected()[0].GetReason().GetCode())
	assert.Equal(t, int32(codes.InvalidArgument), resp.GetRejected()[1].GetReason().GetCode())
	assert.Equal(t, int32(codes.Unimplemented), resp.GetRejected()[2].GetReason().GetCode())
	assert.Empty(t, resp.GetCommittedSeqByRun())
}
