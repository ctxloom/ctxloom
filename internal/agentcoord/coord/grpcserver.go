package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Runner liveness parameters. The runner heartbeats every HeartbeatInterval;
// the coordinator synthesizes RunExited for a runner's active runs after
// RunnerLossHeartbeats MISSED heartbeats (docker-stop kills runner
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
// hash). lastBeat is guarded by Coordinator.mu. send/pending carry
// coordinator-initiated RunnerRequests (StartRun foremost) — the mirror of
// runChan's request plumbing on RunChannel, one direction reversed.
type runnerSession struct {
	credHash string
	runID    string
	lastBeat time.Time
	cancel   context.CancelFunc

	send chan *agentcoordpb.RuntimeFrame

	reqMu   sync.Mutex
	pending map[string]chan *agentcoordpb.RunnerResponse
	// ended marks a session whose pending requests have already been failed:
	// its channel is gone, so nothing will ever resolve a waiter registered
	// after that point. Guarded by reqMu together with pending, because the
	// two decisions ("is this session still answering" and "record my
	// waiter") must be one atomic step — see requestRunner.
	ended bool
}

// newRunnerSession builds a runnerSession ready to register.
func newRunnerSession(credHash, runID string, lastBeat time.Time, cancel context.CancelFunc) *runnerSession {
	return &runnerSession{
		credHash: credHash,
		runID:    runID,
		lastBeat: lastBeat,
		cancel:   cancel,
		send:     make(chan *agentcoordpb.RuntimeFrame, 8),
		pending:  make(map[string]chan *agentcoordpb.RunnerResponse),
	}
}

// failPending resolves every in-flight request with an UNAVAILABLE response
// (the session died before an answer arrived) so no requestRunner caller
// hangs past the session's end. It also marks the session ended, which is what
// keeps a caller that read the session just before its teardown from
// registering a waiter nobody is left to resolve.
func (rs *runnerSession) failPending() {
	rs.reqMu.Lock()
	pending := rs.pending
	rs.pending = make(map[string]chan *agentcoordpb.RunnerResponse)
	rs.ended = true
	rs.reqMu.Unlock()
	for id, ch := range pending {
		ch <- &agentcoordpb.RunnerResponse{RequestId: id, Status: statusErr(codes.Unavailable, "runner session ended before answering")}
	}
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

// coordService implements agentcoord.v1.CoordinatorService: RunnerChannel
// (Wave B1) and RunChannel (Wave C1/B1.6) are both live, and they are the
// service's whole surface.
type coordService struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	c *Coordinator
}

// coordinatorServiceMethodPrefix is every RPC a D1 read-only consumer
// credential must NEVER authenticate — process control, run-level
// mutation, and the event-plane ingress. ConsumerService (consumer.go) is
// deliberately NOT under this prefix: any authenticated identity, consumer
// or otherwise, may watch.
const coordinatorServiceMethodPrefix = "/agentcoord.v1.CoordinatorService/"

// artifactUploadFullMethod is the ONE ArtifactTransferService RPC a D1
// read-only consumer credential must never authenticate (E1a: "consumers
// are read-only" — upload mutates the store). DownloadArtifact is
// deliberately NOT blocked here: consumer-class credentials may read,
// exactly like ConsumerService (coord/artifacts.go's
// authorizeArtifactDownload allows caller.Consumer unconditionally).
const artifactUploadFullMethod = "/agentcoord.v1.ArtifactTransferService/UploadArtifact"

// grpcServer builds the coordinator's gRPC server with per-stream credential
// verification (identity is re-checked per request too — every handler calls
// Identify again rather than trusting a cached principal). D1: a consumer
// credential authenticates ConsumerService only — presenting one on
// RunnerChannel/RunChannel is a rejected identity, not just an
// unauthorized verb, so a leaked viewer credential cannot mutate anything or
// impersonate a runner/child (read-only scope enforced server-side). E1a
// extends the same rule to UploadArtifact (mutating) while leaving
// DownloadArtifact open to consumers (read-only).
func (c *Coordinator) grpcServer() *grpc.Server {
	auth := func(ctx context.Context, fullMethod string) error {
		id, ok := c.Identify(mdToken(ctx))
		if !ok {
			return status.Error(codes.Unauthenticated, "unknown or revoked credential")
		}
		if id.Consumer && (strings.HasPrefix(fullMethod, coordinatorServiceMethodPrefix) || fullMethod == artifactUploadFullMethod) {
			return status.Error(codes.PermissionDenied, "a read-only consumer credential cannot call "+fullMethod)
		}
		return nil
	}
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(func(v any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := auth(ss.Context(), info.FullMethod); err != nil {
				return err
			}
			return handler(v, ss)
		}),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := auth(ctx, info.FullMethod); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
	)
	agentcoordpb.RegisterCoordinatorServiceServer(srv, &coordService{c: c})
	agentcoordpb.RegisterConsumerServiceServer(srv, &consumerService{c: c})
	agentcoordpb.RegisterArtifactTransferServiceServer(srv, &artifactService{c: c})
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
			// The ack carries the reason, not just the refusal: the runner
			// reads the frame before it ever sees this RPC's own status, so
			// an unpopulated reject_reason is the only thing it has to
			// report — see rejectedHelloError (runnerlink.go).
			reason := fmt.Sprintf("run %s was not issued to this credential", runID)
			_ = stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
				HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: false, RejectReason: &rpcstatus.Status{
					Code:    int32(codes.PermissionDenied),
					Message: reason,
				}},
			}})
			return status.Error(codes.PermissionDenied, reason)
		}
	}
	if err := stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
		HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: true},
	}}); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(stream.Context())
	rs := newRunnerSession(credHash, id.RunID, c.now(), cancel)
	c.mu.Lock()
	if prev := c.runners[credHash]; prev != nil {
		prev.cancel() // one RunnerChannel per credential; newest wins (reconnect)
	}
	c.runners[credHash] = rs
	if ready, ok := c.runnerReady[credHash]; ok {
		close(ready)
		delete(c.runnerReady, credHash)
	}
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
		rs.failPending()
		if registered {
			// Disconnect IS runner loss (∪ RunExited): synthesize for the
			// credential's remaining active runs. terminateRun dedupes
			// against the chat-close path exactly-once.
			c.runnerLost(credHash, "RunnerChannel disconnected")
		}
	}()

	// Single writer pump: coordinator-initiated RunnerRequests (StartRun
	// foremost) funnel through rs.send — the same discipline runChan uses
	// on RunChannel, reversed. goTracked: see
	// RunChannel's identical pump in runchannel.go for why this only
	// terminates once the underlying gRPC transport is actually cut
	// (srv.close()'s GracefulStop/Stop), not on c.baseCtx cancellation alone.
	c.goTracked(func() {
		for {
			select {
			case frame := <-rs.send:
				if err := stream.Send(frame); err != nil {
					cancel()
					return
				}
			case <-streamCtx.Done():
				return
			}
		}
	})

	recvErr := make(chan error, 1)
	c.goTracked(func() {
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
			case *agentcoordpb.RunnerFrame_Response:
				rs.reqMu.Lock()
				ch := rs.pending[kind.Response.GetRequestId()]
				delete(rs.pending, kind.Response.GetRequestId())
				rs.reqMu.Unlock()
				if ch != nil {
					ch <- kind.Response
				}
			case *agentcoordpb.RunnerFrame_Hello:
				// A duplicate hello on a live stream is a protocol slip;
				// tolerated as a heartbeat.
				c.mu.Lock()
				rs.lastBeat = c.now()
				c.mu.Unlock()
			}
		}
	})

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
	c.recordHarnessSession(runID, exited.GetHarnessSessionId())
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
		rs.failPending()
		c.runnerLost(rs.credHash, "missed heartbeats past the loss bound")
	}
}

// awaitRunner blocks until credHash's runner registers on RunnerChannel (or
// ctx ends), returning its live session immediately if already connected.
// The StartRun-issuing spawn path uses this: the coordinator spawns the
// runner PROCESS (isolation-prepared, go-plugin-handshaked) and then must
// wait for that same process to dial home before it can StartRun on it.
func (c *Coordinator) awaitRunner(ctx context.Context, credHash string) (*runnerSession, error) {
	c.mu.Lock()
	if rs := c.runners[credHash]; rs != nil {
		c.mu.Unlock()
		return rs, nil
	}
	ready, ok := c.runnerReady[credHash]
	if !ok {
		ready = make(chan struct{})
		c.runnerReady[credHash] = ready
	}
	c.mu.Unlock()
	select {
	case <-ready:
		c.mu.Lock()
		rs := c.runners[credHash]
		c.mu.Unlock()
		if rs == nil {
			return nil, errors.New("coord: runner registration signaled but its session already ended")
		}
		return rs, nil
	case <-ctx.Done():
		c.mu.Lock()
		// Only clean up the waiter WE created and nobody claimed yet — a
		// concurrent awaitRunner for the same credHash (shouldn't happen in
		// practice; one spawn mints one credential) must not lose its wakeup.
		select {
		case <-ready:
		default:
			delete(c.runnerReady, credHash)
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// requestRunner issues one coordinator-initiated RunnerRequest
// (StartRun/StopRun/KillRun/Drain) to the runner connected under credHash and
// waits for its RunnerResponse, bounded by ctx or a default budget. This is
// the RunnerChannel counterpart of Home.Request, direction reversed.
func (c *Coordinator) requestRunner(ctx context.Context, credHash string, req *agentcoordpb.RunnerRequest) (*agentcoordpb.RunnerResponse, error) {
	if req.RequestId == "" {
		req.RequestId = randID("rreq-", 12)
	}
	c.mu.Lock()
	rs := c.runners[credHash]
	c.mu.Unlock()
	if rs == nil {
		return nil, errors.New("coord: no connected runner for this credential")
	}
	ch := make(chan *agentcoordpb.RunnerResponse, 1)
	rs.reqMu.Lock()
	if rs.ended {
		// The session was torn down between the lookup above and here: its
		// pending map has already been swapped out and its send pump is gone,
		// so registering now would buy a full-budget wait for an answer that
		// cannot come.
		rs.reqMu.Unlock()
		return nil, errors.New("coord: the runner session ended before this request could be issued")
	}
	rs.pending[req.RequestId] = ch
	rs.reqMu.Unlock()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	select {
	case rs.send <- &agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_Request{Request: req}}:
	case <-ctx.Done():
		rs.reqMu.Lock()
		delete(rs.pending, req.RequestId)
		rs.reqMu.Unlock()
		return nil, ctx.Err()
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		rs.reqMu.Lock()
		delete(rs.pending, req.RequestId)
		rs.reqMu.Unlock()
		return nil, ctx.Err()
	}
}
