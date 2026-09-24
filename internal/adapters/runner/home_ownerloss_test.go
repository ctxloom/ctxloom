package runner

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// ownerServer is a coordinator reduced to the one thing the owner-loss clock
// reads: a RunnerChannel that accepts the Hello and then holds the link open
// until the server stops. It can be stopped and served again on the SAME
// address, which is the coordinator dying and coming back on its recorded
// endpoint — the link going down and returning under the test's control.
type ownerServer struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	addr   string
	hellos atomic.Int32
	srv    *grpc.Server
}

func (o *ownerServer) RunnerChannel(stream grpc.BidiStreamingServer[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
		HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: true},
	}}); err != nil {
		return err
	}
	o.hellos.Add(1)
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func (o *ownerServer) serve(t *testing.T) {
	t.Helper()
	addr := o.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	o.addr = ln.Addr().String()
	o.srv = grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(o.srv, o)
	go func() { _ = o.srv.Serve(ln) }()
	t.Cleanup(o.srv.Stop)
}

func (o *ownerServer) url() string { return fmt.Sprintf("http://%s/mcp", o.addr) }

func ownerLossHome(t *testing.T, url string, window time.Duration) *Home {
	t.Helper()
	h, err := NewHome(context.Background(), HomeConfig{
		Reporter: termSink(), URL: url, Token: "t", RunID: "run-1", Harness: "mock", Version: "test",
		RedialBackoff:   10 * time.Millisecond,
		OwnerLossWindow: window,
	})
	require.NoError(t, err)
	t.Cleanup(h.Crash)
	return h
}

// TestHome_OwnerLossWindowEndsARunnerWithNoCoordinator: a runner whose
// coordinator never answers declares its owner lost once the window has run
// out — never before it — and stops redialling. Without this a container's
// foreground process redials forever, so --rm never fires.
func TestHome_OwnerLossWindowEndsARunnerWithNoCoordinator(t *testing.T) {
	const window = 300 * time.Millisecond
	start := time.Now()
	h := ownerLossHome(t, "http://127.0.0.1:1/mcp", window)

	select {
	case <-h.OwnerLost():
		require.GreaterOrEqual(t, time.Since(start), window, "the owner is declared lost only once the whole window has passed")
	case <-time.After(conformanceWait):
		t.Fatalf("no coordinator for %s, yet the runner never declared its owner lost", conformanceWait)
	}
}

// TestHome_OwnerLinkBackInsideTheWindowKeepsTheRunner: the link drops and
// returns inside the window (a coordinator restarted on its recorded
// endpoint), so the runner is NOT declared lost — the clock stops while the
// link is up, however long it stays up. A drop after that re-arms the clock
// in full, measured from the second drop.
func TestHome_OwnerLinkBackInsideTheWindowKeepsTheRunner(t *testing.T) {
	const window = time.Second
	owner := &ownerServer{}
	owner.serve(t)
	h := ownerLossHome(t, owner.url(), window)
	require.Eventually(t, func() bool { return owner.hellos.Load() == 1 }, conformanceWait, 5*time.Millisecond)

	owner.srv.Stop() // the coordinator dies...
	owner.serve(t)   // ...and is back on the same endpoint, well inside the window
	h.Redial()
	require.Eventually(t, func() bool { return owner.hellos.Load() == 2 }, conformanceWait, 5*time.Millisecond,
		"the runner must re-Hello the coordinator that came back")

	// Past the window measured from the FIRST drop — and from the dial, too —
	// while the link is up: a clock left running by either would fire here.
	select {
	case <-h.OwnerLost():
		t.Fatal("the link came back inside the window, yet the owner was declared lost")
	case <-time.After(window + window/2):
	}

	secondDrop := time.Now()
	owner.srv.Stop()
	select {
	case <-h.OwnerLost():
		require.GreaterOrEqual(t, time.Since(secondDrop), window, "a later drop gets the whole window again")
	case <-time.After(conformanceWait):
		t.Fatal("the link dropped for good, yet the owner was never declared lost")
	}
}

// TestMain_EndsWhenItsOwnerIsLost: the process half. Main blocks until ctx
// ends OR its home reports the owner lost, and then tears down and returns
// ErrOwnerLost — the return that ends a container's foreground process.
func TestMain_EndsWhenItsOwnerIsLost(t *testing.T) {
	env := &mainEnv{vars: reachEnv("http://127.0.0.1:1/mcp", "t", "run-1")}
	deps := mainDeps(env, func(*EngineHost, *Home) (Deps, error) { return Deps{}, nil })
	deps.OwnerLossWindow = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- Main(context.Background(), deps) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrOwnerLost)
	case <-time.After(conformanceWait):
		t.Fatal("Main kept running with no coordinator past its owner-loss window")
	}
}
