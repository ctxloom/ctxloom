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

// Coordinator is one live coordinator on this host, as discovery finds it:
// its endpoint and the project it serves. Embedding keeps the endpoint's
// credential redaction on every rendering of a Coordinator too.
type Coordinator struct {
	discover.Endpoint
}

// DiscoverCoordinators lists every live coordinator this user can reach,
// from the endpoint files under the coordinator state root, most recently
// active first. skipped names each endpoint file that could not be read: it
// may be a coordinator that is running.
func DiscoverCoordinators() (coordinators []Coordinator, skipped []error) {
	endpoints, skipped := discover.List()
	for _, ep := range endpoints {
		coordinators = append(coordinators, Coordinator{Endpoint: ep})
	}
	return coordinators, skipped
}

// QueryPendingApprovals asks one coordinator for the requests parked at its
// root, over ConsumerService.PendingApprovals. The queue lives only in the
// coordinator process, so this read is the only view another terminal has.
// The error wraps the gRPC status: a coordinator that exited after
// discovery found it is codes.Unavailable, distinct from one that answered
// with a refusal. Bounded by consumerDialTimeout like every other one-shot
// consumer call.
func QueryPendingApprovals(ctx context.Context, c Coordinator) (*agentcoordpb.PendingApprovalsResult, error) {
	client, conn, err := dialConsumer(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("pending approvals: %w", err)
	}
	defer func() { _ = conn.Close() }()

	qctx, cancel := context.WithTimeout(ctx, consumerDialTimeout)
	defer cancel()
	res, err := client.PendingApprovals(qctx, &agentcoordpb.PendingApprovalsRequest{})
	if err != nil {
		return nil, fmt.Errorf("pending approvals at %s: %w", c.URL, err)
	}
	return res, nil
}
