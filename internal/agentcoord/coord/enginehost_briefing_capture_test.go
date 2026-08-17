package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// deafChat honours the StructuredChat contract (closes `out` before
// returning) but never reads `in`, so a briefing send can only ever lose its
// race to ctx.Done().
type deafChat struct{ running chan struct{} }

func (d *deafChat) Chat(ctx context.Context, _ agent.ChatRequest, _ <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	close(d.running)
	<-ctx.Done()
	return ctx.Err()
}

// TestEngineHost_BriefingIsRecordedAsIntentNotAsDelivery pins what the
// canonical transcript MEANS for a run torn down before its engine ever read
// the briefing. A review row read the eager RecordUserText as a transcript that
// "claims a user turn the engine never saw" and wanted the record moved into
// the delivery goroutine's success arm. That trade is not free in either
// direction and the current choice is the deliberate one:
//
//   - Recorded eagerly, the briefing is the FIRST line of the transcript, in
//     the order a reader expects — the engine's own Session event arrives
//     before the engine reads its first message, so recording after the
//     rendezvous would file the user's opening turn AFTER the session it
//     opened.
//   - The transcript is the record of the conversation as ASKED; the run's
//     outcome is plane-1's job, and a run cancelled before delivery reports
//     RUN_STATUS_CANCELLED there. Losing the prompt entirely would leave a
//     cancelled child with no record of what it was asked at all.
//
// So: a briefing that was never delivered is still recorded, and nothing
// else is — no assistant turn is invented alongside it.
func TestEngineHost_BriefingIsRecordedAsIntentNotAsDelivery(t *testing.T) {
	testsupport.Isolate(t)
	home := &fakeEngineHome{}
	dc := &deafChat{running: make(chan struct{})}
	eh := NewEngineHost(context.Background(), dc, "claude-code", "run-1")
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	select {
	case <-dc.running:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend's Chat call never started")
	}

	// Tear the run down with the briefing still undelivered.
	eh.Close()

	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, 5*time.Second, 10*time.Millisecond, "the cancelled run must still report RunExited")

	recs := readCanonicalTranscript(t, "child-harp-1")
	require.Len(t, recs, 1, "exactly the briefing: no invented assistant turn, and the prompt is not lost")
	require.NotNil(t, recs[0].Entry)
	assert.Equal(t, "user", recs[0].Entry.Type)
	assert.Equal(t, "CTX\n\ndo the thing", recs[0].Entry.Content)

	// Plane-1, not the transcript, is where the run's fate is recorded.
	home.mu.Lock()
	defer home.mu.Unlock()
	var status agentcoordpb.Result_RunStatus
	for _, ev := range home.events {
		if rc := ev.GetRunCompleted(); rc != nil {
			status = rc.GetResult().GetStatus()
		}
	}
	assert.Equal(t, agentcoordpb.Result_RUN_STATUS_CANCELLED, status,
		"the cancellation is reported on plane-1; the transcript records what was asked")
}
