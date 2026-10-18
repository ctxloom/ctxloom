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
// accepts both channels, and records what the lifecycle link says. With
// silent set it accepts the lifecycle link and then never reads or answers
// again.
type goodbyeServer struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	silent bool

	mu       sync.Mutex
	exited   *agentcoordpb.RunExited
	attached chan struct{}
	exitedCh chan struct{}
	release  chan struct{}
}

func newGoodbyeServer(silent bool) *goodbyeServer {
	return &goodbyeServer{silent: silent, attached: make(chan struct{}), exitedCh: make(chan struct{}), release: make(chan struct{})}
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
	if s.silent {
		<-s.release
		return nil
	}
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
// signal) and its result channel, once the lifecycle link is attached.
func startSignalledRunner(t *testing.T, srv *goodbyeServer) (context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(gs, srv)
	go func() { _ = gs.Serve(ln) }()
	t.Cleanup(func() { close(srv.release); gs.Stop() })

	env := &mainEnv{vars: reachEnv(t, "http://"+ln.Addr().String()+"/mcp", "t", "run-1")}
	deps := mainDeps(env, func(*EngineHost, *Home) (Deps, error) { return Deps{}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Main(ctx, deps) }()
	select {
	case <-srv.attached:
	case <-time.After(conformanceWait):
		t.Fatal("the runner never attached its lifecycle link")
	}
	return cancel, done
}

// TestMain_ASignalledRunnerStillReportsItsExit: a signal cancels Main's
// context, and the coordinator must still receive RunExited — without it the
// coordinator records a clean exit as runner loss.
func TestMain_ASignalledRunnerStillReportsItsExit(t *testing.T) {
	srv := newGoodbyeServer(false)
	cancel, done := startSignalledRunner(t, srv)
	// The link is attached on the client a moment after the server acked.
	time.Sleep(200 * time.Millisecond)

	cancel()

	select {
	case <-srv.exitedCh:
	case <-time.After(conformanceWait):
		t.Fatal("the coordinator never received RunExited from the signalled runner")
	}
	srv.mu.Lock()
	require.Equal(t, "run-1", srv.exited.GetRunId())
	srv.mu.Unlock()
	require.NoError(t, <-done)
}

// TestMain_ASilentCoordinatorCannotHoldExitPastTheDrainBudget: the goodbye is
// best-effort; a coordinator that accepted the link and then answers nothing
// must not keep the process alive past shutdownDrainBudget.
func TestMain_ASilentCoordinatorCannotHoldExitPastTheDrainBudget(t *testing.T) {
	srv := newGoodbyeServer(true)
	cancel, done := startSignalledRunner(t, srv)
	time.Sleep(200 * time.Millisecond)

	cancel()

	err := testsupport.Within(t, shutdownDrainBudget+2*time.Second, func() error { return <-done },
		"Main outlived the shutdown drain budget against a silent coordinator")
	require.NoError(t, err)
}
