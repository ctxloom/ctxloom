package coord

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// testRunHome is testHome's counterpart for a Home that HOSTS A RUN: cfg.RunID
// is what makes ReportRunExited a run terminal rather than a no-op, and the
// nil stream is the between-reconnects state answerControl already drops into.
func testRunHome(t *testing.T, caps ...string) *Home {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Home{
		cfg:          HomeConfig{RunID: "run-1", Harness: "test", Version: "test", Capabilities: caps},
		ctx:          ctx,
		cancel:       cancel,
		ackCh:        make(chan struct{}),
		pending:      make(map[string]*homeReq),
		consumed:     make(map[string]bool),
		turnPending:  make(map[string]bool),
		inflightCtrl: make(map[string]*inflightCtrl),
	}
}

func (h *Home) inflightCtrlLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.inflightCtrl)
}

// Home.inflightCtrl had no delete ANYWHERE in the package: every control
// request a runner ever served left a record behind for the life of the
// process, each retaining a full AgentResponse — for a Summarize or a
// Question, the model's entire answer. Its coordinator-side mirror
// (Coordinator.reqTrack) has always been pruned at the run terminal by
// clearReqTrack; this half now mirrors that.
//
// The test pins BOTH halves of the contract, because the cheap "fix" —
// evicting entries as they age — breaks the expensive one: a reissue whose
// record was evicted RE-DISPATCHES, which is a second child turn nobody asked
// for.
func TestHomeControl_InflightRecordsClearedAtTheRunTerminal(t *testing.T) {
	h := testRunHome(t, CapSteer)

	var dispatches atomic.Int32
	h.SetRequestHandler(func(_ context.Context, _ *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
		dispatches.Add(1)
		return &agentcoordpb.AgentResponse{}
	})

	for _, id := range []string{"ctl-1", "ctl-2", "ctl-3"} {
		h.serveCoordinatorRequest(steerRequest(id, "instruction", CapSteer))
	}
	// waitTracked JOINS the dispatch goroutines rather than polling for them:
	// a negative assertion ("no second dispatch") is only worth making once
	// the dispatch that would have happened is provably finished.
	h.waitTracked()
	require.Equal(t, int32(3), dispatches.Load(), "all three control requests must reach the executor")
	require.Equal(t, 3, h.inflightCtrlLen(), "each served request is recorded")

	// BEFORE the terminal the record is load-bearing: a reissue (a reconnect
	// dropped the answer) must be answered from it, NOT re-executed.
	h.serveCoordinatorRequest(steerRequest("ctl-2", "instruction", CapSteer))
	h.waitTracked()
	assert.Equal(t, int32(3), dispatches.Load(),
		"a reissue before the run terminal must not start a second dispatch — that is a second child turn")

	// THE TERMINAL. Past it the coordinator settles the whole down direction
	// (clearDownTrack) and reissues nothing, so no answer is owed any more.
	h.ReportRunExited(0, "")

	assert.Equal(t, 0, h.inflightCtrlLen(),
		"the run terminal must drop this run's responder-side idempotency records — without it the map grows "+
			"without bound for the life of the runner process, each entry holding a whole model answer")
}

// A session-owner runner hosts no run of its own, so it has no run terminal to
// clear at — ReportRunExited is a no-op for it, and must stay one.
func TestHomeControl_SessionOwnerHomeHasNoRunTerminal(t *testing.T) {
	h := testRunHome(t, CapSteer)
	h.cfg.RunID = ""
	h.SetRequestHandler(func(_ context.Context, _ *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
		return &agentcoordpb.AgentResponse{}
	})
	h.serveCoordinatorRequest(steerRequest("ctl-1", "instruction", CapSteer))
	h.waitTracked()
	require.Equal(t, 1, h.inflightCtrlLen())

	h.ReportRunExited(0, "")
	assert.Equal(t, 1, h.inflightCtrlLen(), "a runner with no run of its own has no terminal here")
}
