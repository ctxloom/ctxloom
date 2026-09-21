package coordgrpc

import (
	"context"

	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// consumerService implements agentcoord.v1.ConsumerService (D1): additive,
// read-only, no change to CoordinatorService. Both RPCs also work called
// in-process (no gRPC hop) via the Coordinator methods below — D3's acp
// session loop, hosting the coordinator library itself, uses that path.
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
