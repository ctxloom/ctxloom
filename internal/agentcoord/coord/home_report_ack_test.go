package coord

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// ackDroppingCoordinator serves RunChannel the way the real coordinator does
// on its unhappy path: it journals what it receives and then FAILS TO DELIVER
// the Ack — the coordinator's ack send is non-blocking and drops the frame when
// the outbound buffer is full ("cumulative acks make that safe").
//
// It answers a re-issued event exactly as the real coordinator does: a seq at or
// below the highest already processed is not re-journaled, it re-acks the
// durable watermark (handleAgentEvent's dedupe arm).
type ackDroppingCoordinator struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer

	mu      sync.Mutex
	maxSeq  uint64
	reissue int
}

func (f *ackDroppingCoordinator) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return nil
	}
	if err := stream.Send(&agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
		HelloAck: &agentcoordpb.HelloAck{Accepted: true},
	}}); err != nil {
		return err
	}
	for {
		frame, rerr := stream.Recv()
		if rerr != nil {
			return rerr
		}
		ev := frame.GetEvent()
		if ev == nil {
			continue
		}
		f.mu.Lock()
		duplicate := ev.GetSeq() <= f.maxSeq
		if duplicate {
			f.reissue++
		} else {
			f.maxSeq = ev.GetSeq() // journaled and fsynced; the ack is then DROPPED
		}
		watermark := f.maxSeq
		f.mu.Unlock()
		if !duplicate {
			continue
		}
		if serr := stream.Send(&agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Ack{
			Ack: &agentcoordpb.Ack{CommittedSeq: watermark},
		}}); serr != nil {
			return serr
		}
	}
}

// serveAckDroppingCoordinator stands the fake up and returns its URL.
func serveAckDroppingCoordinator(t *testing.T) (*ackDroppingCoordinator, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fake := &ackDroppingCoordinator{}
	srv := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(srv, fake)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return fake, "http://" + ln.Addr().String() + MCPPath
}

// TestReport_SurvivesADroppedAck: a report the coordinator has durably
// journaled must be reported as filed. Report waits for a cumulative Ack that
// the coordinator is explicitly allowed to DROP, and nothing else on a live
// channel ever prompts another one — so a dropped watermark turned a successful,
// durable filing into a full-budget wait ending in "the coordinator accepted
// this request and may still be running it", the one answer that invites a
// duplicate retry of work already committed.
func TestReport_SurvivesADroppedAck(t *testing.T) {
	fake, url := serveAckDroppingCoordinator(t)

	h, err := NewHome(context.Background(), HomeConfig{URL: url, Token: "t", Harness: "mock", Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	require.Eventually(t, h.Attached, 10*time.Second, 10*time.Millisecond, "the run channel must attach")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	err = h.Report(ctx, &agentcoordpb.Summary{Text: "done"}, nil)

	require.NoError(t, err, "a durably journaled report must not answer failure because its Ack was lost")
	assert.Less(t, time.Since(start), 15*time.Second, "the recovery must not wait out the request budget")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Positive(t, fake.reissue, "the runner must re-issue the unacked event to prompt a fresh watermark")
}
