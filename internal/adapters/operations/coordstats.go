package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// QueryCoordinatorSpoolStats asks ONE live coordinator for its spool counters
// over ConsumerService.SpoolStats. The counters exist only in the coordinator
// process (no journal fact records them), so this is the only read an
// out-of-process diagnostic has — and an endpoint whose coordinator has
// exited answers with an error, never with zeros that would read as healthy.
// Bounded by consumerDialTimeout like every other one-shot consumer call.
func QueryCoordinatorSpoolStats(ctx context.Context, ep discover.Endpoint) (*agentcoordpb.SpoolStatsResult, error) {
	client, conn, err := dialConsumer(ep)
	if err != nil {
		return nil, fmt.Errorf("spool stats: %w", err)
	}
	defer func() { _ = conn.Close() }()

	qctx, cancel := context.WithTimeout(ctx, consumerDialTimeout)
	defer cancel()
	res, err := client.SpoolStats(qctx, &agentcoordpb.SpoolStatsRequest{})
	if err != nil {
		return nil, fmt.Errorf("spool stats at %s: %w", ep.URL, err)
	}
	return res, nil
}

// QueryPendingApprovals asks ONE live coordinator for the requests parked
// at its root, over ConsumerService.PendingApprovals. The queue lives only
// in the coordinator process, so this read is the only view another
// terminal has. The error wraps the gRPC status: an endpoint whose
// coordinator has exited is codes.Unavailable, distinct from a coordinator
// that answered with a refusal.
func QueryPendingApprovals(ctx context.Context, ep discover.Endpoint) (*agentcoordpb.PendingApprovalsResult, error) {
	client, conn, err := dialConsumer(ep)
	if err != nil {
		return nil, fmt.Errorf("pending approvals: %w", err)
	}
	defer func() { _ = conn.Close() }()

	qctx, cancel := context.WithTimeout(ctx, consumerDialTimeout)
	defer cancel()
	res, err := client.PendingApprovals(qctx, &agentcoordpb.PendingApprovalsRequest{})
	if err != nil {
		return nil, fmt.Errorf("pending approvals at %s: %w", ep.URL, err)
	}
	return res, nil
}
