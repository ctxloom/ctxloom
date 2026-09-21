package coord

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// RunChannel is the run-level stream: opened by the runner for each run it
// hosts (and by the session owner's runner with an empty run_id). All agent
// traffic — plane-2 requests, plane-1 events, plane-3 notices — multiplexes
// here; identity derives from the connection credential. The handler only
// decodes and calls the coordinator: the handshake's verdict, every inbound
// frame's meaning and every outbound frame's content are the coordinator's.
func (s *coordService) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	c := s.c
	done, ok := c.streams.enter()
	if !ok {
		return status.Error(codes.Unavailable, "coordinator is closing")
	}
	defer done() // registered FIRST so it runs LAST, after the teardown below
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return status.Error(codes.InvalidArgument, "first AgentFrame must be Hello")
	}
	hello := RunHelloFromWire(first.GetHello())
	streamCtx, cancel := context.WithCancel(stream.Context())
	ch, err := c.AttachRun(id, hello, cancel)
	if err != nil {
		cancel()
		reason := fmt.Sprintf("run %q was not issued to this credential", hello.RunID)
		if !errors.Is(err, ErrRunNotIssued) {
			reason = err.Error()
		}
		_ = stream.Send(&agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
			HelloAck: &agentcoordpb.HelloAck{Accepted: false, RejectReason: StatusErr(codes.PermissionDenied, reason)},
		}})
		return status.Error(codes.PermissionDenied, reason)
	}
	defer c.ReleaseRun(ch)
	if err := stream.Send(&agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
		HelloAck: &agentcoordpb.HelloAck{
			Accepted: true,
			// NOT an independent watermark: the coordinator keeps no durable
			// event log, so it echoes the runner's own claim back. The proto
			// says so at Hello.resume_from_seq / HelloAck.committed_seq's
			// doc — nobody should build a client that trusts this as
			// confirmation.
			CommittedSeq: hello.ResumeFromSeq,
		},
	}}); err != nil {
		return err
	}

	// Single writer pump: everything outbound funnels through the channel's
	// queue. goTracked terminates once streamCtx is cancelled — either locally
	// (a newer reconnect, or ReleaseRun's cancel) or when the server tears
	// the underlying gRPC transport down (streamCtx derives from the STREAM's
	// context, not c.baseCtx, so only the server actually cutting the
	// transport unblocks a still-live channel — see Coordinator.Close's doc).
	c.goTracked(func() {
		ch.Pump(streamCtx, func(f OutFrame) error {
			frame := OutFrameToWire(f)
			if frame == nil {
				return nil
			}
			return stream.Send(frame)
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
			handleAgentFrame(c, ch, frame)
		}
	})

	select {
	case err := <-recvErr:
		return err
	case <-streamCtx.Done():
		return status.Error(codes.Canceled, "run channel closed")
	}
}

// handleAgentFrame decodes one inbound frame and hands it to the
// coordinator.
func handleAgentFrame(c *Coordinator, ch *RunChannel, frame *agentcoordpb.AgentFrame) {
	switch kind := frame.GetKind().(type) {
	case *agentcoordpb.AgentFrame_Event:
		c.HandleEvent(ch, EventFromWire(kind.Event))
	case *agentcoordpb.AgentFrame_Request:
		req, err := AgentRequestFromWire(kind.Request)
		if err != nil {
			c.RefuseRequest(ch, req.RequestID, decodeRefusal(err))
			return
		}
		c.HandleRequest(ch, req)
	case *agentcoordpb.AgentFrame_Heartbeat:
		// Plane-3 liveness; RunnerChannel owns loss detection.
	case *agentcoordpb.AgentFrame_Hello:
		// Duplicate hello on a live stream: tolerated.
	case *agentcoordpb.AgentFrame_SpoolChanged:
		ref, err := SpoolRefFromProto(kind.SpoolChanged)
		if err != nil {
			c.RefuseSpoolChanged(ch, err)
			return
		}
		c.HandleSpoolChanged(ch, ref)
	}
}

// decodeRefusal is the transport's own refusal of a frame that cannot mean
// a request: an unsupported kind keeps its sentinel (UNIMPLEMENTED), every
// other decode failure is the caller's argument (INVALID_ARGUMENT).
func decodeRefusal(err error) error {
	if errors.Is(err, ErrUnsupportedRequest) || errors.Is(err, ErrPeerSendIsLocal) {
		return err
	}
	return refusal(ErrInvalidRequest, "%s", err.Error())
}
