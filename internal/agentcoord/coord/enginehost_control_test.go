package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestEnqueueTurn_KeepsTheTagFifoInSendOrder pins the turn-attribution
// substrate. Turn order equals send order only because enqueueTurn serializes
// the sends; a FIFO that can be pushed out of band is a FIFO that will
// eventually attribute a control turn's output to the wrong request, which is
// how a question returns someone else's answer.
func TestEnqueueTurn_KeepsTheTagFifoInSendOrder(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.Equal(t, int32(0), eh.Handle(&agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())

	// The briefing's tag is pushed synchronously at startRun, so it is always
	// first regardless of when its send lands.
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	ctx := context.Background()
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{mail: "m-a"}, "first"))
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{}, "second"))
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{mail: "m-c"}, "third"))

	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 4 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"CTX\n\ndo the thing", "first", "second", "third"}, sc.recordedTexts())

	// Every enqueued turn was popped by the adapt loop, in order, leaving the
	// FIFO empty rather than drifting one entry per turn.
	require.Eventually(t, func() bool {
		eh.mu.Lock()
		defer eh.mu.Unlock()
		return len(eh.pendingTags) == 0
	}, 5*time.Second, 10*time.Millisecond, "the FIFO must drain with the turns, not accumulate")
}

// TestEnqueueTurn_RefusesBeforeAnyRunStarted: there is no turn stream yet, and
// silently returning nil would let a control verb report success for a turn
// nothing will ever take.
func TestEnqueueTurn_RefusesBeforeAnyRunStarted(t *testing.T) {
	eh := NewEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	err := eh.enqueueTurn(context.Background(), turnTag{}, "nowhere to go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no run has started")
}
