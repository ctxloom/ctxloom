package runner

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// goodbyeServer is a coordinator double for a whole runner process: it
// accepts both channels, and records what the lifecycle link says.
type goodbyeServer struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer

	mu       sync.Mutex
	exited   *agentcoordpb.RunExited
	attached chan struct{}
	exitedCh chan struct{}
	release  chan struct{}
}

func newGoodbyeServer() *goodbyeServer {
	return &goodbyeServer{attached: make(chan struct{}), exitedCh: make(chan struct{}), release: make(chan struct{})}
}

func (s *goodbyeServer) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(helloAck(true, nil)); err != nil {
		return err
	}
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func (s *goodbyeServer) RunnerChannel(stream grpc.BidiStreamingServer[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: true}}}); err != nil {
		return err
	}
	close(s.attached)
	for {
		f, err := stream.Recv()
		if err != nil {
			return err
		}
		if ex := f.GetRunExited(); ex != nil {
			s.mu.Lock()
			s.exited = ex
			s.mu.Unlock()
			close(s.exitedCh)
			return nil
		}
	}
}

// startSignalledRunner runs Main against srv and returns its cancel (the
// signal), its result channel and its Home, once the lifecycle link is
// attached.
func startSignalledRunner(t *testing.T, srv *goodbyeServer) (context.CancelFunc, <-chan error, *Home) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(gs, srv)
	go func() { _ = gs.Serve(ln) }()
	t.Cleanup(func() { close(srv.release); gs.Stop() })

	env := &mainEnv{vars: reachEnv(t, "http://"+ln.Addr().String()+"/mcp", "t", "run-1")}
	homes := make(chan *Home, 1)
	deps := mainDeps(env, func(_ *EngineHost, h *Home) (Deps, error) { homes <- h; return Deps{}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Main(ctx, deps) }()
	select {
	case <-srv.attached:
	case <-time.After(conformanceWait):
		t.Fatal("the runner never attached its lifecycle link")
	}
	return cancel, done, <-homes
}

// stallLink holds the attached lifecycle link's send lock until the test
// ends, which is what a write blocked on a coordinator that stopped reading
// does to every other sender: home.Close blocks inside link.Shutdown.
func stallLink(t *testing.T, h *Home) {
	t.Helper()
	h.mu.Lock()
	link := h.link
	h.mu.Unlock()
	require.NotNil(t, link, "the home has no lifecycle link to stall")
	link.sendMu.Lock()
	t.Cleanup(link.sendMu.Unlock)
}

// TestMain_AStalledGoodbyeCannotHoldExitPastTheDrainBudget: the goodbye is
// best-effort. When home.Close itself stalls writing to a coordinator that
// stopped reading, Main must still return within shutdownDrainBudget.
func TestMain_AStalledGoodbyeCannotHoldExitPastTheDrainBudget(t *testing.T) {
	srv := newGoodbyeServer()
	cancel, done, home := startSignalledRunner(t, srv)
	time.Sleep(200 * time.Millisecond)
	stallLink(t, home)

	start := time.Now()
	cancel()

	err := testsupport.Within(t, shutdownDrainBudget+2*time.Second, func() error { return <-done },
		"Main outlived the shutdown drain budget while home.Close was stalled")
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), shutdownDrainBudget, "the stall must actually have been waited out by the bound")
}
