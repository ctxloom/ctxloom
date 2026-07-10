package coord

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

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
	runID   string
	conn    *grpc.ClientConn
	stream  grpc.BidiStreamingClient[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]
	sendMu  sync.Mutex // serializes stream.Send (single-writer discipline)
	cancel  context.CancelFunc
	done    chan struct{}
	handler RunnerRequestHandler
}

// send writes one frame under the single-writer mutex.
func (l *RunnerLink) send(frame *agentcoordpb.RunnerFrame) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.stream.Send(frame)
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
	stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunnerChannel(linkCtx)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("coord: open RunnerChannel: %w", err)
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
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("coord: RunnerHello: %w", err)
	}
	ack, err := stream.Recv()
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("coord: RunnerHelloAck: %w", err)
	}
	if ha := ack.GetHelloAck(); ha == nil || !ha.GetAccepted() {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("coord: RunnerHello rejected")
	}

	l := &RunnerLink{runID: runID, conn: conn, stream: stream, cancel: cancel, done: make(chan struct{}), handler: handler}
	go l.heartbeatLoop(linkCtx)
	go l.receiveLoop()
	return l, nil
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
			go l.serveRequest(kind.Request)
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
// chance; the coordinator's synthesis is the load-bearing path) and closes
// the link.
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
	_ = l.stream.CloseSend()
	l.cancel()
	<-l.done
	_ = l.conn.Close()
}
