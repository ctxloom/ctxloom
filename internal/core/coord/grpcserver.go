package coord

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

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
// and RunChannel are the service's whole surface. It only decodes and calls
// the coordinator.
type coordService struct {
	agentcoordpb.UnimplementedCoordinatorServiceServer
	c *Coordinator
}

// coordinatorServiceMethodPrefix is every RPC a read-only consumer
// credential must NEVER authenticate — process control, run-level
// mutation, and the event-plane ingress. ConsumerService is deliberately
// NOT under this prefix: any authenticated identity, consumer or otherwise,
// may watch.
const coordinatorServiceMethodPrefix = "/agentcoord.v1.CoordinatorService/"

// artifactUploadFullMethod is the ONE ArtifactTransferService RPC a
// read-only consumer credential must never authenticate ("consumers are
// read-only" — upload mutates the store). DownloadArtifact is deliberately
// NOT blocked here: consumer-class credentials may read, exactly like
// ConsumerService (Coordinator.OpenArtifact allows a consumer
// unconditionally).
const artifactUploadFullMethod = "/agentcoord.v1.ArtifactTransferService/UploadArtifact"

// grpcServer builds the coordinator's gRPC server with per-stream credential
// verification (identity is re-checked per request too — every handler calls
// Identify again rather than trusting a cached principal). A consumer
// credential authenticates ConsumerService only — presenting one on
// RunnerChannel/RunChannel is a rejected identity, not just an
// unauthorized verb, so a leaked viewer credential cannot mutate anything or
// impersonate a runner/child (read-only scope enforced server-side). The same
// rule covers UploadArtifact (mutating) while leaving DownloadArtifact open to
// consumers (read-only).
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

// encodeStartRunLaunch projects a StartRun's launch for the wire — the
// launch codec is coordgrpc's.
func encodeStartRunLaunch(sr StartRun) *agentcoordpb.Launch {
	return coordgrpc.EncodeLaunch(sr.Launch)
}

// RunnerChannel is the runner-level control channel: one per runner process,
// dialed by the runner. Identity derives from the connection credential —
// RunnerHello carries only capabilities and active runs, never an identity
// claim. The handler decodes, asks the coordinator for the handshake's
// verdict, and from then on only pumps and decodes.
func (s *coordService) RunnerChannel(stream grpc.BidiStreamingServer[agentcoordpb.RunnerFrame, agentcoordpb.RuntimeFrame]) error {
	c := s.c
	done, ok := c.streams.enter()
	if !ok {
		return status.Error(codes.Unavailable, "coordinator is closing")
	}
	defer done() // registered FIRST so it runs LAST, after the teardown below
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
	if first.GetHello() == nil {
		return status.Error(codes.InvalidArgument, "first RunnerFrame must be RunnerHello")
	}
	hello := RunnerHelloFromWire(first.GetHello())
	if err := c.RunnerHello(credHash, hello); err != nil {
		// The ack carries the reason, not just the refusal: the runner
		// reads the frame before it ever sees this RPC's own status, so
		// an unpopulated reject_reason is the only thing it has to
		// report.
		code := codes.PermissionDenied
		if errors.Is(err, ErrDraining) {
			code = codes.Unavailable
		}
		_ = stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
			HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: false, RejectReason: StatusErr(code, err.Error())},
		}})
		return status.Error(code, err.Error())
	}
	if err := stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_HelloAck{
		HelloAck: &agentcoordpb.RunnerHelloAck{Accepted: true},
	}}); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(stream.Context())
	rs := c.AttachRunner(id, credHash, cancel)
	defer c.DetachRunner(rs)

	// Single writer pump: coordinator-initiated requests (StartRun foremost)
	// funnel through the session's queue — the same discipline the run
	// channel uses, reversed. goTracked: the pump only terminates once the
	// underlying gRPC transport is actually cut (the server's
	// GracefulStop/Stop), not on c.baseCtx cancellation alone.
	c.goTracked(func() {
		rs.Pump(streamCtx, func(req RunnerRequest) error {
			return stream.Send(&agentcoordpb.RuntimeFrame{Kind: &agentcoordpb.RuntimeFrame_Request{Request: RunnerRequestToWire(req, encodeStartRunLaunch)}})
		})
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
				c.RunnerHeartbeat(rs)
			case *agentcoordpb.RunnerFrame_RunExited:
				c.RunnerExited(rs.CredHash(), RunExitedFromWire(kind.RunExited))
			case *agentcoordpb.RunnerFrame_Response:
				rs.Resolve(RunnerResponseFromWire(kind.Response))
			case *agentcoordpb.RunnerFrame_Hello:
				// A duplicate hello on a live stream is a protocol slip;
				// tolerated as a heartbeat.
				c.RunnerHeartbeat(rs)
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
