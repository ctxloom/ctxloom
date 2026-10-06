package runner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// countingRecorder counts what the engine host records into the transcript.
type countingRecorder struct{ n atomic.Int32 }

func (r *countingRecorder) Record(agent.ChatEvent) error { r.n.Add(1); return nil }
func (r *countingRecorder) Close() error                 { return nil }

// TestEngineHost_StartTurnRefusedOnceSealedUndoesItsTurn: startTurn publishes
// the turn (its attribution tag, the busy gate, its cancel) before
// dispatching it. Refused, the turn never runs, so all of that is taken back
// — a waiter on turnBusy would otherwise wait for a turn that never ends —
// and no user turn lands in the transcript.
func TestEngineHost_StartTurnRefusedOnceSealedUndoesItsTurn(t *testing.T) {
	eh := NewEngineHost(context.Background(), nil, "claude-code", "run-1")
	rec := &countingRecorder{}
	eh.runCtx = context.Background()
	eh.rec = rec
	eh.tracked.Seal()

	err := eh.startTurn(turnTag{mail: "msg-1"}, "do the thing")
	require.ErrorIs(t, err, errEngineHostClosed)

	eh.mu.Lock()
	defer eh.mu.Unlock()
	assert.Empty(t, eh.pendingTags, "the refused turn's attribution tag is still queued")
	assert.Nil(t, eh.turnBusy, "the refused turn still reads as busy")
	assert.Zero(t, rec.n.Load(), "a refused turn recorded a user turn")
}

// capturingStream is lateRequestStream that keeps what the link sends.
type capturingStream struct {
	*lateRequestStream
	mu   sync.Mutex
	sent []*agentcoordpb.RunnerFrame
}

func (s *capturingStream) Send(f *agentcoordpb.RunnerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, f)
	return nil
}

// TestRunnerLink_RequestRefusedOnceSealedIsAnsweredUnavailable: a request the
// receive loop cannot dispatch is answered Unavailable on the spot, never
// served — so the coordinator's waiter learns the link is going rather than
// waiting out its own timeout.
func TestRunnerLink_RequestRefusedOnceSealedIsAnsweredUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the stub hands over its one request, then reports the stream dead
	s := &capturingStream{lateRequestStream: &lateRequestStream{ctx: ctx}}
	var served atomic.Bool
	l := &RunnerLink{
		stream: s,
		done:   make(chan struct{}),
		handler: func(*agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
			served.Store(true)
			return &agentcoordpb.RunnerResponse{}
		},
	}
	l.tracked.Seal()

	l.receiveLoop()

	assert.False(t, served.Load(), "a request refused by the sealed link was served")
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.sent, 1, "the refused request was not answered")
	resp := s.sent[0].GetResponse()
	require.NotNil(t, resp)
	assert.Equal(t, "rreq-late", resp.GetRequestId())
	assert.Equal(t, int32(codes.Unavailable), resp.GetStatus().GetCode())
}
