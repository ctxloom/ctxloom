package coordgrpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// consumerService implements agentcoord.v1.ConsumerService: additive,
// read-only, no change to CoordinatorService. Each RPC projects the
// coordinator's own in-process form.
type consumerService struct {
	agentcoordpb.UnimplementedConsumerServiceServer
	c *coord.Coordinator
}

func (s *consumerService) ListRuns(_ context.Context, req *agentcoordpb.ListRunsRequest) (*agentcoordpb.ListRunsResult, error) {
	return RunsSnapshotToWire(s.c.ListRuns(req.GetIncludeTerminal(), req.GetRole())), nil
}

// SpoolStats is the unary read of the coordinator's process-lifetime spool
// counters — the three in-process accessors (SpoolDeliveryStats,
// SpoolDoorbellStats, PushUnavailableCount) projected onto one wire message.
// No journal fact records any of these (they are outcome tallies, not
// state), so this RPC is the ONLY way a process that does not host the
// coordinator can see them.
func (s *consumerService) SpoolStats(context.Context, *agentcoordpb.SpoolStatsRequest) (*agentcoordpb.SpoolStatsResult, error) {
	return SpoolStatsToWire(s.c.SpoolStats()), nil
}

// PendingApprovals projects the root's approval queue for a viewer in
// another terminal. Only the consumer credential reads it: the auth
// interceptor admits any identity to ConsumerService, and an agent is shown
// that a child waits, never what it asks. The project is the caller's
// identity's, which the coordinator stamps with the one project it serves.
func (s *consumerService) PendingApprovals(ctx context.Context, _ *agentcoordpb.PendingApprovalsRequest) (*agentcoordpb.PendingApprovalsResult, error) {
	id, ok := s.c.Identify(mdToken(ctx))
	if !ok || !id.Consumer {
		return nil, status.Error(codes.PermissionDenied, "pending approvals are read with the consumer credential only")
	}
	return PendingApprovalsToWire(s.c.Approvals().Pending(), id.ProjectDir), nil
}

// WatchRuns serves the stream: snapshot first, then live AgentEvents
// (subscribe BEFORE building the snapshot so nothing published in the gap
// between subscribing and sending is missed — it simply arrives, correctly
// ordered, right after the snapshot frame instead of before).
func (s *consumerService) WatchRuns(req *agentcoordpb.WatchRunsRequest, stream grpc.ServerStreamingServer[agentcoordpb.WatchEvent]) error {
	c := s.c
	snapshot, events, cancel, _ := c.WatchRuns(req.GetRunIds())
	defer cancel()

	snap := RunsSnapshotToWire(snapshot)
	if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Snapshot{Snapshot: &agentcoordpb.RosterSnapshot{Runs: snap.GetRuns()}}}); err != nil {
		return err
	}
	ctx := stream.Context()
	for {
		select {
		case ev := <-events:
			if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Event{Event: EventToWire(ev)}}); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
