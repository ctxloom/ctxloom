package coord

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// RunnerLink is the runner side of RunnerChannel: `ctxloom llm serve` dials
// home when its process env carries the coordinator trio. It sends
// RunnerHello (capabilities + active runs — never an identity claim),
// heartbeats on HeartbeatInterval, and a best-effort RunExited at shutdown.
// The transport is plaintext HTTP/2 (h2c) on a loopback/bridge interface,
// authenticated by the bearer credential; TLS arrives with the cert/mTLS
// slice (Wave E) — the delivery layer treats the credential as opaque, so
// that is a mint/verify change, not plumbing.
type RunnerLink struct {
	runID  string
	conn   *grpc.ClientConn
	stream grpc.BidiStreamingClient[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]
	cancel context.CancelFunc
	done   chan struct{}
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
// minted run this runner hosts (CTXLOOM_RUN_ID). Returns an error when the
// coordinator is unreachable or the Hello is rejected — callers treat that
// as a warning, never a launch blocker (the coordinator's synthesis covers a
// runner that never dialed).
func DialRunner(ctx context.Context, coordURL, token, runID, harness, version string) (*RunnerLink, error) {
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
	if err := stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{
		Hello: &agentcoordpb.RunnerHello{
			Version:           version,
			Harnesses:         []string{harness},
			MaxConcurrentRuns: 1,
			ActiveRunIds:      []string{runID},
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

	l := &RunnerLink{runID: runID, conn: conn, stream: stream, cancel: cancel, done: make(chan struct{})}
	go l.heartbeatLoop(linkCtx)
	return l, nil
}

func (l *RunnerLink) heartbeatLoop(ctx context.Context) {
	defer close(l.done)
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := l.stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Heartbeat{
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

// Shutdown sends a best-effort RunExited (docker-stop usually gives no
// chance; the coordinator's synthesis is the load-bearing path) and closes
// the link.
func (l *RunnerLink) Shutdown(exitCode int, harnessSessionID string) {
	_ = l.stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_RunExited{
		RunExited: &agentcoordpb.RunExited{
			RunId:            l.runID,
			ExitCode:         int32(exitCode),
			HarnessSessionId: harnessSessionID,
		},
	}})
	_ = l.stream.CloseSend()
	l.cancel()
	<-l.done
	_ = l.conn.Close()
}
