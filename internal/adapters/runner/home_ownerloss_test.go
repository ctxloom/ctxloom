package runner

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	t.Setenv(sessions.EnvRunnerOwnerLossWindow, "200ms")
	deps := mainDeps(env, func(*EngineHost, *Home) (Deps, error) { return Deps{}, nil })
	done := make(chan error, 1)
	go func() { done <- Main(context.Background(), deps) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrOwnerLost)
	case <-time.After(conformanceWait):
		t.Fatal("Main kept running with no coordinator past its owner-loss window")
	}
}

// TestMain_OwnerLossWindowOverride: the operator's override is what the home
// runs on; a set-but-invalid one (unparseable, zero, negative) falls back to
// DefaultOwnerLossWindow and says so LOUDLY, naming the variable; unset is
// the default, silently.
func TestMain_OwnerLossWindowOverride(t *testing.T) {
	for _, c := range []struct {
		raw  string
		set  bool
		want time.Duration
		warn bool
	}{
		{raw: "5m", set: true, want: 5 * time.Minute},
		{raw: "nope", set: true, want: DefaultOwnerLossWindow, warn: true},
		{raw: "0s", set: true, want: DefaultOwnerLossWindow, warn: true},
		{raw: "-1m", set: true, want: DefaultOwnerLossWindow, warn: true},
		{set: false, want: DefaultOwnerLossWindow},
	} {
		t.Run(c.raw, func(t *testing.T) {
			if c.set {
				t.Setenv(sessions.EnvRunnerOwnerLossWindow, c.raw)
			} else {
				t.Setenv(sessions.EnvRunnerOwnerLossWindow, "")
			}
			var sink lockedFindings
			env := &mainEnv{vars: reachEnv("http://127.0.0.1:1/mcp", "t", "run-1")}
			got := make(chan time.Duration, 1)
			deps := mainDeps(env, func(_ *EngineHost, h *Home) (Deps, error) { got <- h.cfg.OwnerLossWindow; return Deps{}, nil })
			deps.Reporter = &sink
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- Main(ctx, deps) }()
			require.Equal(t, c.want, <-got)
			cancel()
			require.NoError(t, <-done)
			var warned bool
			found := sink.all()
			for _, f := range found {
				if strings.Contains(f.Text, sessions.EnvRunnerOwnerLossWindow) {
					warned = true
				}
			}
			require.Equal(t, c.warn, warned, "findings: %v", found)
		})
	}
}

// TestHome_AHangingDialDoesNotStopTheClock: a coordinator that accepts the
// RunnerChannel and never answers the Hello — wedged, or a half-open peer —
// must not hold the owner-loss clock. The dial is cut off when the window
// runs out, and the owner is declared lost on time.
func TestHome_AHangingDialDoesNotStopTheClock(t *testing.T) {
	addr := serveWedged(t, "", wedgedOwner{})

	const window = 300 * time.Millisecond
	start := time.Now()
	h := ownerLossHome(t, fmt.Sprintf("http://%s/mcp", addr), window)
	select {
	case <-h.OwnerLost():
		require.GreaterOrEqual(t, time.Since(start), window)
	case <-time.After(conformanceWait):
		t.Fatal("a dial that never completes held the owner-loss clock")
	}
}

// awaitOwnerAttached blocks until the RUNNER holds the lifecycle link. The
// server counting a Hello is not that: it counts after sending the HelloAck,
// so a stop that lands before the runner reads the ack fails the dial instead
// of dropping a link — and only a dropped link refills the owner-loss budget.
// The budget the runner spent idle across that dial stays spent.
func awaitOwnerAttached(t *testing.T, h *Home) {
	t.Helper()
	select {
	case <-h.ownerPresent():
	case <-time.After(conformanceWait):
		t.Fatal("the runner never attached its lifecycle link")
	}
}

// wedgedOwner takes the RunnerChannel and never answers the Hello. opened, if
// set, is told each time a dial arrives (without blocking the dial).
type wedgedOwner struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	opened chan struct{}
}

func (w wedgedOwner) RunnerChannel(stream grpc.BidiStreamingServer[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]) error {
	if w.opened != nil {
		select {
		case w.opened <- struct{}{}:
		default:
		}
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

// serveWedged serves a wedgedOwner on addr ("" = any port) and returns its
// address.
func serveWedged(t *testing.T, addr string, w wedgedOwner) string {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	srv := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(srv, w)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

// TestHome_ATurnStartedMidDialPausesTheClockThen: the clock pauses when a turn
// starts, even while a dial is in flight — not when the dial gives up. A
// wedged owner holds the dial the whole budget left; charging all of it would
// leave nothing after the turn, and the runner would be declared lost the
// moment the turn ends.
func TestHome_ATurnStartedMidDialPausesTheClockThen(t *testing.T) {
	const window = 400 * time.Millisecond
	addr := serveWedged(t, "", wedgedOwner{})
	h := ownerLossHome(t, fmt.Sprintf("http://%s/mcp", addr), window)

	time.Sleep(window / 2) // half the window spent waiting, inside the first dial
	h.setTurning(true)
	select {
	case <-h.OwnerLost():
		t.Fatal("a turn making progress was cut off")
	case <-time.After(2 * window): // well past that dial's cutoff
	}
	ended := time.Now()
	h.setTurning(false)

	select {
	case <-h.OwnerLost():
		require.GreaterOrEqual(t, time.Since(ended), window/4,
			"about half the window was left when the turn started; the turn's time was charged as waiting")
	case <-time.After(conformanceWait):
		t.Fatal("the owner never came back, yet it was never declared lost")
	}
}

// TestHome_ATurnEndedMidDialStartsTheClockThen: the mirror — the clock starts
// when a turn ends, even mid-dial, not when that dial gives up.
func TestHome_ATurnEndedMidDialStartsTheClockThen(t *testing.T) {
	const window = 500 * time.Millisecond
	owner := &ownerServer{}
	owner.serve(t)
	h := ownerLossHome(t, owner.url(), window)
	awaitOwnerAttached(t, h)
	h.setTurning(true)
	owner.srv.Stop()
	opened := make(chan struct{}, 16)
	serveWedged(t, owner.addr, wedgedOwner{opened: opened})

	// The first dial to arrive may have been in flight since before the wedged
	// owner was up; the next one starts fresh, so it is known to start here.
	for range 2 {
		select {
		case <-opened:
		case <-time.After(conformanceWait):
			t.Fatal("the runner stopped redialling during a turn")
		}
	}
	ended := time.Now()
	h.setTurning(false)

	select {
	case <-h.OwnerLost():
		elapsed := time.Since(ended)
		require.GreaterOrEqual(t, elapsed, window)
		require.Less(t, elapsed, window+window/2,
			"the clock started when the turn ended mid-dial, not when that dial gave up")
	case <-time.After(conformanceWait):
		t.Fatal("the owner never came back, yet it was never declared lost")
	}
}

// TestHome_ATurnBlockedOnTheCoordinatorIsWaiting: a turn in progress pauses
// the owner-loss clock only while it makes progress. Blocked on a
// coordinator-bound request (Home.Request, which waits up to
// coord.DefaultRequestTimeout and is reissued on reconnect) it is waiting on
// its owner, and the clock runs.
func TestHome_ATurnBlockedOnTheCoordinatorIsWaiting(t *testing.T) {
	const window = 300 * time.Millisecond
	owner := &ownerServer{}
	owner.serve(t)
	h := ownerLossHome(t, owner.url(), window)
	awaitOwnerAttached(t, h)
	h.setTurning(true)
	owner.srv.Stop()

	// Progress: the clock is paused.
	select {
	case <-h.OwnerLost():
		t.Fatal("a turn making progress was cut off")
	case <-time.After(3 * window):
	}

	blocked := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	go func() {
		_, _ = h.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}}})
	}()
	select {
	case <-h.OwnerLost():
		require.GreaterOrEqual(t, time.Since(blocked), window, "the clock starts when the turn blocks on its owner")
	case <-time.After(conformanceWait):
		t.Fatal("a turn blocked on a coordinator request never counted as waiting")
	}
}

// TestDeliverNotice_NoWakeOrNudgeWhileTheOwnerIsAway: the session owner's
// engine takes mail through a wake or a terminal nudge — each starts a turn.
// With the owner away neither fires; mail buffers, and the owner's return
// fires once for what waited.
func TestDeliverNotice_NoWakeOrNudgeWhileTheOwnerIsAway(t *testing.T) {
	h := newNoticeHome(t)
	h.ownerUp, h.present = false, make(chan struct{})
	var nudges atomic.Int32
	h.SetTerminalNudge(func() { nudges.Add(1) })

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-away"})
	require.Zero(t, nudges.Load(), "no nudge — no new turn — while the owner is away")
	require.Equal(t, 1, h.BufferedMailCount())

	h.setOwnerPresent(true)
	require.Equal(t, int32(1), nudges.Load(), "the owner's return fires for the mail that waited")
}

// lockedFindings is a Sink a running Home may report into from several
// goroutines at once; report.Findings is for a synchronous caller and races.
type lockedFindings struct {
	mu sync.Mutex
	fs report.Findings
}

func (l *lockedFindings) Report(f report.Finding) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fs = append(l.fs, f)
}

func (l *lockedFindings) all() report.Findings {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append(report.Findings(nil), l.fs...)
}
