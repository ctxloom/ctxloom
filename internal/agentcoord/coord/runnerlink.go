package coord

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// rejectedHelloError renders a refused handshake using the coordinator's own
// reject_reason. Both handshake acks carry one so a refusal is actionable,
// and both callers reach it through this single renderer.
//
// The reason is what separates a permanent refusal from a transient one, and
// both handshakes are inside forever-retrying loops: a caller that reports
// only "rejected" leaves an operator watching a channel that never attaches,
// with nothing to distinguish a stale run id from a revoked credential or a
// coordinator that is simply down. An ack that refuses without saying why is
// itself named, rather than smoothed over — a silent field is a defect on the
// far side, not a normal outcome.
func rejectedHelloError(what string, reason *rpcstatus.Status) error {
	if msg := reason.GetMessage(); msg != "" {
		return fmt.Errorf("%s rejected: %s (%s)", what, msg, codes.Code(reason.GetCode()))
	}
	if reason != nil {
		return fmt.Errorf("%s rejected: %s", what, codes.Code(reason.GetCode()))
	}
	return fmt.Errorf("%s rejected, and the coordinator sent no reject_reason", what)
}

// RunnerRequestHandler answers one coordinator-initiated RunnerRequest
// (StartRun/StopRun/KillRun/Drain) — the runner's engine-control seam. It
// runs on its own goroutine per request (a StartRun can take real time to
// spawn the harness) and must not block the link's single receive loop.
type RunnerRequestHandler func(*agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse

// RunnerLink is the runner side of RunnerChannel: `ctxloom llm serve` dials
// home when its process env carries the coordinator trio. It sends
// RunnerHello (capabilities + active runs — never an identity claim),
// heartbeats on HeartbeatInterval, a best-effort RunExited at shutdown, and —
// since Wave C — receives coordinator-initiated RunnerRequests (StartRun
// foremost) and answers them through the supplied handler. The transport is
// plaintext HTTP/2 (h2c) on a loopback/bridge interface, authenticated by the
// bearer credential; TLS arrives with the cert/mTLS slice (Wave E) — the
// delivery layer treats the credential as opaque, so that is a mint/verify
// change, not plumbing.
type RunnerLink struct {
	runID  string
	conn   *grpc.ClientConn
	stream grpc.BidiStreamingClient[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]
	sendMu sync.Mutex // serializes stream.Send AND CloseSend (see closeSend)
	// sendClosed records that the send side has been half-closed, so a sender
	// that acquires sendMu afterwards refuses rather than calling SendMsg on a
	// stream that can no longer take one. Guarded by sendMu, because it is
	// exactly the state CloseSend and SendMsg contend over.
	sendClosed bool
	cancel     context.CancelFunc
	done       chan struct{}
	handler    RunnerRequestHandler

	// tracked owns every goroutine DialRunner/receiveLoop dispatches beyond its
	// spawning call's own return (heartbeatLoop, receiveLoop itself, and one
	// serveRequest per coordinator-initiated RunnerRequest). Shutdown joins it
	// before closing the underlying conn — mirrors the Coordinator's, Home's and
	// EngineHost's groups: an unjoined
	// serveRequest racing Shutdown's conn.Close could still be mid-Send on a
	// torn-down transport.
	tracked trackedGroup
}

// ErrLinkSendClosed refuses a frame written after the link's send side was
// half-closed.
//
// It is typed and it is OURS rather than gRPC's: grpc-go answers a SendMsg
// after CloseSend with codes.Internal ("SendMsg called after CloseSend"),
// which reads in a log as a library fault rather than as this process having
// asked for something impossible during its own orderly shutdown.
var ErrLinkSendClosed = errors.New("coord: this runner link's send side is closed (shutting down)")

// send writes one frame under the single-writer mutex.
//
// The mutex serializes senders against EACH OTHER and against closeSend, which
// is not a stylistic choice: gRPC's ClientStream contract permits one
// concurrent reader and one concurrent writer and NOTHING else — SendMsg from
// two goroutines, or CloseSend concurrent with SendMsg, is a data race inside
// the stream's own state, not a queueing question.
func (l *RunnerLink) send(frame *agentcoordpb.RunnerFrame) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	if l.sendClosed {
		return ErrLinkSendClosed
	}
	return l.stream.Send(frame)
}

// closeSend half-closes the stream's send side under the SAME mutex every
// send takes.
//
// THIS IS THE FIX FOR A CONFIRMED DATA RACE. CloseSend is a SEND-SIDE
// operation: it writes the stream's own sent-last state, which SendMsg reads
// and writes too. Shutdown used to call it unserialized while the heartbeat
// loop was still ticking — the two are on different goroutines by
// construction, so the window is not exotic — and the detector caught exactly
// that pair (CloseSend at Shutdown vs SendMsg under heartbeatLoop).
//
// The flag matters as much as the lock. Serializing alone would leave a
// heartbeat that won the mutex AFTER the half-close calling SendMsg on a
// closed send side, which gRPC answers as a caller bug; refusing it here means
// the last thing a shutting-down link does on the wire is its half-close.
func (l *RunnerLink) closeSend() {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	if l.sendClosed {
		return
	}
	l.sendClosed = true
	_ = l.stream.CloseSend()
}

// grpcTarget derives the gRPC dial target from the coordinator URL (the gRPC
// server rides the same host:port as the MCP endpoint — one h2c listener,
// content-type routed).
func grpcTarget(coordURL string) (string, error) {
	u, err := url.Parse(coordURL)
	if err != nil {
		return "", fmt.Errorf("coord: parse %s: %w", EnvCoordURL, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("coord: %s %q has no host", EnvCoordURL, coordURL)
	}
	return u.Host, nil
}

// bearerCreds attaches the credential to every RPC (per-RPC metadata; the
// server re-verifies per request).
type bearerCreds string

func (b bearerCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearerCreds) RequireTransportSecurity() bool { return false }

// DialRunner opens the RunnerChannel and completes the Hello handshake.
// harness names the backend this runner drives; runID is the coordinator-
// minted run this runner hosts (CTXLOOM_RUN_ID). handler answers
// coordinator-initiated RunnerRequests (StartRun/StopRun/KillRun/Drain); nil
// is valid for a link that never expects one (e.g. a test dialing only to
// observe lifecycle). Returns an error when the coordinator is unreachable or
// the Hello is rejected — callers treat that as a warning, never a launch
// blocker (the coordinator's synthesis covers a runner that never dialed).
func DialRunner(ctx context.Context, coordURL, token, runID, harness, version string, handler RunnerRequestHandler) (*RunnerLink, error) {
	target, err := grpcTarget(coordURL)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	if err != nil {
		return nil, fmt.Errorf("coord: dial coordinator %s: %w", target, err)
	}
	linkCtx, cancel := context.WithCancel(ctx)
	// unwind is the single teardown for every failure past this point. A conn
	// left open on a failure path has no owner: the caller's redial loop only
	// ever Shutdowns a link DialRunner actually handed back.
	unwind := func(format string, a ...any) (*RunnerLink, error) {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf(format, a...)
	}
	stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunnerChannel(linkCtx)
	if err != nil {
		return unwind("coord: open RunnerChannel: %w", err)
	}
	// A session-owner runner (empty runID) hosts no spawned run: advertise
	// no active runs rather than an empty id the ownership check rejects.
	var active []string
	if runID != "" {
		active = []string{runID}
	}
	if err := stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{
		Hello: &agentcoordpb.RunnerHello{
			Version:           version,
			Harnesses:         []string{harness},
			MaxConcurrentRuns: 1,
			ActiveRunIds:      active,
		},
	}}); err != nil {
		return unwind("coord: RunnerHello: %w", err)
	}
	ack, err := stream.Recv()
	if err != nil {
		return unwind("coord: RunnerHelloAck: %w", err)
	}
	ha := ack.GetHelloAck()
	if ha == nil {
		return unwind("coord: first RuntimeFrame was not a RunnerHelloAck")
	}
	if !ha.GetAccepted() {
		return unwind("coord: %w", rejectedHelloError("RunnerHello", ha.GetRejectReason()))
	}

	l := &RunnerLink{runID: runID, conn: conn, stream: stream, cancel: cancel, done: make(chan struct{}), handler: handler}
	l.goTracked(func() { l.heartbeatLoop(linkCtx) })
	l.goTracked(l.receiveLoop)
	return l, nil
}

// runnerLinkCloseJoinBudget bounds Shutdown's wait for tracked goroutines —
// see Coordinator's closeJoinBudget (coordinator.go) for the identical
// reasoning: every tracked goroutine here selects on either linkCtx (bound
// to the caller's ctx, already cancelled by the time Shutdown calls
// l.cancel()) or the stream's own Recv, which CloseSend + conn state
// unblocks around the same point.
const runnerLinkCloseJoinBudget = 3 * time.Second

// goTracked runs fn on a new goroutine Shutdown joins before closing the conn —
// see trackedGroup. receiveLoop can dispatch a serveRequest that arrives just as
// Shutdown begins, which is what the seal is for.
func (l *RunnerLink) goTracked(fn func()) { l.tracked.dispatch(fn) }

// waitTracked joins every l.goTracked goroutine, with a bounded escape.
func (l *RunnerLink) waitTracked() {
	l.tracked.wait(runnerLinkCloseJoinBudget, "runner link shutdown", "")
}

func (l *RunnerLink) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := l.send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Heartbeat{
				Heartbeat: &agentcoordpb.RunnerHeartbeat{ActiveRuns: 1},
			}}); err != nil {
				clidiag.Warn("ctxloom", "runner: heartbeat: %v (coordinator will synthesize loss)", err)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// receiveLoop is the link's SINGLE reader: it owns Done (closed when the
// stream dies, by send failure elsewhere or a read error here) and dispatches
// every inbound RuntimeFrame. RunnerRequests (StartRun foremost) run the
// handler on their OWN goroutine — a StartRun can take real time to spawn the
// harness and must not stall this loop, which is also how heartbeats and
// RunExited keep flowing during a spawn.
func (l *RunnerLink) receiveLoop() {
	defer close(l.done)
	for {
		frame, err := l.stream.Recv()
		if err != nil {
			if l.stream.Context().Err() == nil {
				clidiag.WarnOnce("ctxloom", "runner: RunnerChannel receive: %v (coordinator will synthesize loss)", err)
			}
			return
		}
		switch kind := frame.GetKind().(type) {
		case *agentcoordpb.RuntimeFrame_Request:
			l.goTracked(func() { l.serveRequest(kind.Request) })
		case *agentcoordpb.RuntimeFrame_HelloAck:
			// Duplicate ack on a live stream; ignore.
		}
	}
}

// serveRequest answers one coordinator-initiated RunnerRequest. A nil handler
// (or one that returns nil) answers UNIMPLEMENTED rather than dropping the
// request silently — the coordinator's pending waiter would otherwise hang
// until its own timeout.
func (l *RunnerLink) serveRequest(req *agentcoordpb.RunnerRequest) {
	var resp *agentcoordpb.RunnerResponse
	if l.handler != nil {
		resp = l.handler(req)
	}
	if resp == nil {
		resp = &agentcoordpb.RunnerResponse{Status: statusErr(codes.Unimplemented, "runner has no handler for this request")}
	}
	resp.RequestId = req.GetRequestId()
	if err := l.send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Response{Response: resp}}); err != nil {
		clidiag.Warn("ctxloom", "runner: reply to %s: %v", req.GetRequestId(), err)
	}
}

// Done reports the link's end: closed when the receive loop exits (a read
// error or the stream context ending). Home's redial loop watches it.
func (l *RunnerLink) Done() <-chan struct{} { return l.done }

// Shutdown sends a best-effort RunExited (docker-stop usually gives no
// chance; the coordinator's synthesis is the load-bearing path), joins
// every tracked goroutine (heartbeatLoop, receiveLoop, and any
// in-flight serveRequest), then closes the link.
func (l *RunnerLink) Shutdown(exitCode int, harnessSessionID string) {
	if l.runID != "" { // a session-owner runner has no run to report exited
		_ = l.send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_RunExited{
			RunExited: &agentcoordpb.RunExited{
				RunId:            l.runID,
				ExitCode:         int32(exitCode),
				HarnessSessionId: harnessSessionID,
			},
		}})
	}
	l.tracked.seal()
	// SERIALIZED WITH EVERY SENDER (closeSend). The heartbeat loop is still
	// ticking at this point — deliberately, because the half-close below is
	// what makes this a graceful end rather than a cancellation, so it must
	// happen BEFORE l.cancel() kills the stream. Ordering the two this way is
	// what left the window the race detector found; the lock is what closes
	// it without giving the graceful sequence up.
	l.closeSend()
	l.cancel()
	<-l.done
	l.waitTracked()
	_ = l.conn.Close()
}
