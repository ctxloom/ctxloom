package coord

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Runner liveness parameters. The runner heartbeats every HeartbeatInterval;
// the coordinator synthesizes RunExited for a runner's active runs after
// RunnerLossHeartbeats MISSED heartbeats (review R3: docker-stop kills runner
// and harness together, so RunExited is never sent in the motivating
// scenario — synthesis on disconnect or heartbeat silence is the load-bearing
// path). With N=3 at 5s the silent-loss detection bound is 20s
// (interval×(N+1)), checked by a watchdog ticking every interval — so queue
// drain after a silent runner death starts within 25s; a DISCONNECT (the
// docker-stop case) synthesizes immediately.
const (
	HeartbeatInterval    = 5 * time.Second
	RunnerLossHeartbeats = 3
	runnerLossTimeout    = HeartbeatInterval * (RunnerLossHeartbeats + 1)
)

// runnerSession tracks one connected RunnerChannel (keyed by credential
// hash). lastBeat is guarded by Coordinator.mu.
type runnerSession struct {
	credHash string
	runID    string
	lastBeat time.Time
	cancel   context.CancelFunc
}

// mdToken extracts the bearer token from gRPC metadata.
func mdToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get("authorization") {
		if tok, found := strings.CutPrefix(v, "Bearer "); found {
			return tok
		}
	}
	return ""
}

// coordService implements agentcoord.v1.CoordinatorService. Only
// RunnerChannel is live in Wave B1; RunChannel and PublishEvents land with
// Wave C and answer UNIMPLEMENTED.
type coordService struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	c *Coordinator
}

// grpcServer builds the coordinator's gRPC server with per-stream credential
// verification (identity is re-checked per request too — every handler calls
// Identify again rather than trusting a cached principal).
func (c *Coordinator) grpcServer() *grpc.Server {
	auth := func(ctx context.Context) (Identity, error) {
		id, ok := c.Identify(mdToken(ctx))
		if !ok {
			return Identity{}, status.Error(codes.Unauthenticated, "unknown or revoked credential")
		}
		return id, nil
	}
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(func(v any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if _, err := auth(ss.Context()); err != nil {
				return err
			}
			return handler(v, ss)
		}),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if _, err := auth(ctx); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
	)
	agentcoordpb.RegisterCoordinatorServiceServer(srv, &coordService{c: c})
	return srv
}

// RunnerChannel is the runner-level control channel: one per runner process,
// dialed by the runner. Identity derives from the connection credential —
// RunnerHello carries only capabilities and active runs, never an identity
// claim (rev-6 A1).
func (s *coordService) RunnerChannel(stream grpc.BidiStreamingServer[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]) error {
	c := s.c
	// Per-stream-establishment verification + identity mapping.
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	credHash := hashToken(mdToken(stream.Context()))

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first RunnerFrame must be RunnerHello")
	}
	// Ownership: every claimed active run must have been issued to THIS
	// credential (run_id ownership is validated against the issuing
	// credential; presenting someone else's run_id is a rejected Hello).
	for _, runID := range hello.GetActiveRunIds() {
		owned := false
		c.runs.View(func() {
			if r := c.runsF.run(runID); r != nil && r.CredHash == credHash {
				owned = true
			}
		})
		if !owned {
			_ = stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
				HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: false},
			}})
			return status.Errorf(codes.PermissionDenied, "run %s was not issued to this credential", runID)
		}
	}
	if err := stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
		HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: true},
	}}); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(stream.Context())
	rs := &runnerSession{credHash: credHash, runID: id.RunID, lastBeat: c.now(), cancel: cancel}
	c.mu.Lock()
	if prev := c.runners[credHash]; prev != nil {
		prev.cancel() // one RunnerChannel per credential; newest wins (reconnect)
	}
	c.runners[credHash] = rs
	c.mu.Unlock()
	c.audit("runner_hello", id.Harp, map[string]string{"run_id": id.RunID})

	defer func() {
		c.mu.Lock()
		registered := c.runners[credHash] == rs
		if registered {
			delete(c.runners, credHash)
		}
		c.mu.Unlock()
		cancel()
		if registered {
			// Disconnect IS runner loss (∪ RunExited): synthesize for the
			// credential's remaining active runs. terminateRun dedupes
			// against the chat-close path exactly-once.
			c.runnerLost(credHash, "RunnerChannel disconnected")
		}
	}()

	recvErr := make(chan error, 1)
	go func() {
		for {
			frame, rerr := stream.Recv()
			if rerr != nil {
				recvErr <- rerr
				return
			}
			switch kind := frame.GetKind().(type) {
			case *agentcoordpb.RunnerFrame_Heartbeat:
				c.mu.Lock()
				rs.lastBeat = c.now()
				c.mu.Unlock()
			case *agentcoordpb.RunnerFrame_RunExited:
				c.handleRunExited(credHash, kind.RunExited)
			case *agentcoordpb.RunnerFrame_Hello:
				// A duplicate hello on a live stream is a protocol slip;
				// tolerated as a heartbeat.
				c.mu.Lock()
				rs.lastBeat = c.now()
				c.mu.Unlock()
			case *agentcoordpb.RunnerFrame_Response:
				// No coordinator-initiated requests exist in B1.
			}
		}
	}()

	select {
	case err := <-recvErr:
		return err
	case <-streamCtx.Done():
		return status.Error(codes.Canceled, "runner session closed")
	}
}

// handleRunExited processes an explicit process-level exit fact from the
// runner: validate ownership, record the harness resume handle, terminate.
func (c *Coordinator) handleRunExited(credHash string, exited *agentcoordpb.RunExited) {
	runID := exited.GetRunId()
	owned := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && r.CredHash == credHash {
			owned = true
		}
	})
	if !owned {
		clidiag.Warn("ctxloom", "coordinator: RunExited for %s from a credential that does not own it; ignored", runID)
		return
	}
	if sid := exited.GetHarnessSessionId(); sid != "" {
		if err := c.runs.Exec(func() ([]Fact, error) {
			return []Fact{factAt(factRunHarness, c.now(), runHarness{RunID: runID, HarnessSessionID: sid})}, nil
		}); err != nil {
			clidiag.Warn("ctxloom", "coordinator: record harness session id: %v", err)
		}
	}
	detail := ""
	if sig := exited.GetSignal(); sig != "" {
		detail = "signal " + sig
	}
	c.terminateRun(runID, CauseRunnerExit, detail)
}

// runnerLost synthesizes RunExited for every active run issued to the lost
// credential — queue drain and terminal-record synthesis key on RUNNER LOSS
// (disconnect ∪ heartbeat silence ∪ explicit RunExited), reconciled
// exactly-once with the chat-close path inside terminateRun.
func (c *Coordinator) runnerLost(credHash, why string) {
	var active []string
	c.runs.View(func() { active = c.runsF.activeRunsForCred(credHash) })
	for _, runID := range active {
		c.terminateRun(runID, CauseRunnerLoss, why)
	}
}

// runnerWatchdog scans connected runners every HeartbeatInterval and declares
// loss after runnerLossTimeout of heartbeat silence.
func (c *Coordinator) runnerWatchdog() {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.checkRunnerLiveness(c.now())
		case <-c.baseCtx.Done():
			return
		}
	}
}

// checkRunnerLiveness is the watchdog body, callable with an explicit now for
// deterministic tests.
func (c *Coordinator) checkRunnerLiveness(now time.Time) {
	var lost []*runnerSession
	c.mu.Lock()
	for hash, rs := range c.runners {
		if now.Sub(rs.lastBeat) > runnerLossTimeout {
			delete(c.runners, hash)
			lost = append(lost, rs)
		}
	}
	c.mu.Unlock()
	for _, rs := range lost {
		rs.cancel()
		c.runnerLost(rs.credHash, "missed heartbeats past the loss bound")
	}
}
