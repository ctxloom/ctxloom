package runner

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// runChannelServer scripts one RunChannel: it records the Hello, answers
// with first (or fails the stream with failErr when first is nil), then
// records `collect` further frames and ends the stream.
type runChannelServer struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	first   *agentcoordpb.CoordinatorFrame
	failErr error
	collect int

	hello *agentcoordpb.Hello
	got   []*agentcoordpb.AgentFrame
}

func (s *runChannelServer) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	f, err := stream.Recv()
	if err != nil {
		return err
	}
	s.hello = f.GetHello()
	if s.first == nil {
		return s.failErr
	}
	if err := stream.Send(s.first); err != nil {
		return err
	}
	for range s.collect {
		f, err := stream.Recv()
		if err != nil {
			return err
		}
		s.got = append(s.got, f)
	}
	return nil
}

// runChannelClient serves s on loopback and returns a client for it.
func runChannelClient(t *testing.T, s *runChannelServer) agentcoordpb.CoordinatorServiceClient {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(srv, s)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return agentcoordpb.NewCoordinatorServiceClient(conn)
}

// runChannelOnceBounded is h.runChannelOnce with a bound of its own. The
// callers' watchdogs tear the Home down, but that only ends a loop that
// returns when Recv fails; one that ignored the failure would spin past the
// teardown, so the call itself is bounded too.
func runChannelOnceBounded(t *testing.T, h *Home, client agentcoordpb.CoordinatorServiceClient) error {
	t.Helper()
	return testsupport.Within(t, 2*conformanceWait, func() error { return h.runChannelOnce(client) },
		"runChannelOnce did not return once its stream ended")
}

func helloAck(accepted bool, reason *rpcstatus.Status) *agentcoordpb.CoordinatorFrame {
	return &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
		HelloAck: &agentcoordpb.HelloAck{Accepted: accepted, RejectReason: reason},
	}}
}

// TestRunChannelOnce_HandshakeFailures pins each way the Hello exchange ends
// before attaching: the error returned, and that the stream is never adopted.
func TestRunChannelOnce_HandshakeFailures(t *testing.T) {
	cases := []struct {
		name    string
		srv     *runChannelServer
		wantErr string
	}{
		{
			name:    "stream fails before the ack",
			srv:     &runChannelServer{failErr: status.Error(codes.Unavailable, "sealed")},
			wantErr: "rpc error: code = Unavailable desc = sealed",
		},
		{
			name:    "first frame is not a HelloAck",
			srv:     &runChannelServer{first: &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Ack{Ack: &agentcoordpb.Ack{}}}},
			wantErr: "first CoordinatorFrame was not a HelloAck",
		},
		{
			name:    "rejected with a reason",
			srv:     &runChannelServer{first: helloAck(false, &rpcstatus.Status{Code: int32(codes.PermissionDenied), Message: "not yours"})},
			wantErr: "run channel Hello rejected: not yours (PermissionDenied)",
		},
		{
			name:    "rejected without a reason",
			srv:     &runChannelServer{first: helloAck(false, nil)},
			wantErr: "run channel Hello rejected, and the coordinator sent no reject_reason",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := recvHome(t)
			err := runChannelOnceBounded(t, h, runChannelClient(t, tc.srv))
			require.EqualError(t, err, tc.wantErr)
			require.Equal(t, "run-1", tc.srv.hello.GetRunId())
			h.mu.Lock()
			defer h.mu.Unlock()
			require.Nil(t, h.stream)
			require.False(t, h.everAttached)
		})
	}
}

// TestRunChannelOnce_AttachReissuesThenDetaches: an accepted Hello attaches
// the stream, sends the attach Heartbeat FIRST and unconditionally (the
// coordinator's cue to re-sweep out/ — Coordinator.ConfirmAttach), then
// reissues, in order, every unacked event, every outstanding
// request with its original id, and — because a recv is parked — a fresh
// parked event. When the coordinator ends the stream the error is returned
// and the stream is detached.
func TestRunChannelOnce_AttachReissuesThenDetaches(t *testing.T) {
	h := recvHome(t)
	h.emitCustomEvent("ctxloom/one", nil)
	h.emitCustomEvent("ctxloom/two", nil)
	_, ok := h.requests.Register("req-1", &agentcoordpb.AgentRequest{RequestId: "req-1"})
	require.True(t, ok)
	h.mu.Lock()
	h.parked = true
	h.mu.Unlock()

	srv := &runChannelServer{first: helloAck(true, nil), collect: 5}
	// A frame never sent would leave the fake waiting; tearing the Home down
	// turns that hang into a failed assertion below.
	watchdog := time.AfterFunc(conformanceWait, h.cancel)
	defer watchdog.Stop()
	err := runChannelOnceBounded(t, h, runChannelClient(t, srv))
	require.ErrorIs(t, err, io.EOF)

	require.Equal(t, "run-1", srv.hello.GetRunId())
	require.Equal(t, uint32(1), srv.hello.GetProtocolVersion())
	require.Zero(t, srv.hello.GetResumeFromSeq())
	require.Equal(t, h.helloCapabilities(), srv.hello.GetCapabilities())

	require.Len(t, srv.got, 5)
	require.Equal(t, "run-1", srv.got[0].GetHeartbeat().GetRunId(), "the attach Heartbeat precedes the reissue")
	srv.got = srv.got[1:]
	require.Equal(t, "ctxloom/one", srv.got[0].GetEvent().GetCustom().GetName())
	require.Equal(t, uint64(1), srv.got[0].GetEvent().GetSeq())
	require.Equal(t, "ctxloom/two", srv.got[1].GetEvent().GetCustom().GetName())
	require.Equal(t, uint64(2), srv.got[1].GetEvent().GetSeq())
	require.Equal(t, "req-1", srv.got[2].GetRequest().GetRequestId())
	require.Equal(t, coord.CustomRecvParked, srv.got[3].GetEvent().GetCustom().GetName())
	require.Equal(t, uint64(3), srv.got[3].GetEvent().GetSeq())

	h.mu.Lock()
	defer h.mu.Unlock()
	require.Nil(t, h.stream, "the stream is detached once the channel ends")
	require.True(t, h.everAttached)
}
