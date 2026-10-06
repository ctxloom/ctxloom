package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// lateRequestStream is a stub RunnerChannel stream whose ONE inbound request
// lands the instant the link's teardown cancels it: Recv blocks on the stream
// context and, once it ends, hands back a RunnerRequest before reporting the
// stream dead. That is the frame a real stream delivered just as Abort began —
// the window is opened by the stub, not hoped for.
type lateRequestStream struct {
	ctx  context.Context
	once sync.Once
}

func (s *lateRequestStream) Recv() (*agentcoordpb.RuntimeFrame, error) {
	<-s.ctx.Done()
	var frame *agentcoordpb.RuntimeFrame
	s.once.Do(func() {
		frame = &agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_Request{
			Request: &agentcoordpb.RunnerRequest{RequestId: "rreq-late"},
		}}
	})
	if frame != nil {
		return frame, nil
	}
	return nil, s.ctx.Err()
}
func (s *lateRequestStream) Send(*agentcoordpb.RunnerFrame) error { return nil }
func (s *lateRequestStream) CloseSend() error                     { return nil }
func (s *lateRequestStream) Header() (metadata.MD, error)         { return nil, nil }
func (s *lateRequestStream) Trailer() metadata.MD                 { return nil }
func (s *lateRequestStream) Context() context.Context             { return s.ctx }
func (s *lateRequestStream) SendMsg(any) error                    { return nil }
func (s *lateRequestStream) RecvMsg(any) error                    { return nil }

// abortWatchWindow bounds how long the test watches for Abort returning while
// the request it received is still being served. Its EXPIRY is the fixed
// behaviour — Abort is parked joining the serve — so it is an observation
// budget, never a synchronisation device (closeSendWatchWindow's reasoning).
const abortWatchWindow = 250 * time.Millisecond

// TestRunnerLink_AbortJoinsARequestReceivedAsTeardownBegins pins that every
// request the receive loop dispatches is one Abort joins.
//
// The defect it forces: Abort sealed its tracked group BEFORE ending the
// receive loop, and a sealed group still RUNS what is dispatched to it, only
// untracked. A request that landed in that gap was served after Abort — and
// so Home.Crash and Home.Close — had returned: an engine host handling a
// StartRun for a runner already torn down, writing under a HOME its owner was
// removing (a coord test's TempDir cleanup failing on "directory not empty",
// with the runner's late "reply to rreq-…: EOF" printed after the test).
func TestRunnerLink_AbortJoinsARequestReceivedAsTeardownBegins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.NewClient("passthrough:///127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	entered, release := make(chan struct{}), make(chan struct{})
	var served bool
	var mu sync.Mutex
	l := &RunnerLink{
		conn:   conn,
		stream: &lateRequestStream{ctx: ctx},
		cancel: cancel,
		done:   make(chan struct{}),
		handler: func(*agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
			close(entered)
			<-release
			mu.Lock()
			served = true
			mu.Unlock()
			return &agentcoordpb.RunnerResponse{}
		},
	}
	l.goTracked(l.receiveLoop)

	abortDone := make(chan struct{})
	go func() { defer close(abortDone); l.Abort() }()

	select {
	case <-entered:
	case <-time.After(conformanceWait):
		t.Fatal("the request that landed as teardown began was never served — this test proves nothing")
	}

	returnedWhileServing := false
	select {
	case <-abortDone:
		returnedWhileServing = true
	case <-time.After(abortWatchWindow):
	}
	close(release)
	select {
	case <-abortDone:
	case <-time.After(conformanceWait):
		t.Fatal("Abort never returned after the serve was released")
	}

	assert.False(t, returnedWhileServing,
		"Abort returned while a request its receive loop dispatched was still being served: the serve escaped the join")
	mu.Lock()
	defer mu.Unlock()
	assert.True(t, served, "Abort returned before the serve it joined had finished")
}
