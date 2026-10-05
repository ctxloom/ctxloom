package operations

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// QueryPendingApprovals asks every live coordinator on this host for the
// requests parked at its root, over ConsumerService.PendingApprovals. The
// queue lives only in the coordinator process, so this read is the only view
// another terminal has.
//
// A coordinator that is not there has nothing pending: an endpoint nobody
// answers on (codes.Unavailable) is neither an answer nor a failure. A
// failure is a live coordinator that answered with an error — or, when no
// coordinator answered at all, an endpoint file discovery could not read,
// since that may be the one coordinator that is running.
func QueryPendingApprovals(ctx context.Context) (answers []*agentcoordpb.PendingApprovalsResult, failures []error) {
	endpoints, skipped := discover.List()
	for _, ep := range endpoints {
		res, err := queryPendingApprovals(ctx, ep)
		switch {
		case err == nil:
			answers = append(answers, res)
		case status.Code(err) != codes.Unavailable:
			failures = append(failures, err)
		}
	}
	if len(answers) == 0 {
		failures = append(failures, skipped...)
	}
	return answers, failures
}

// queryPendingApprovals is one coordinator's answer, bounded by
// consumerDialTimeout like every other one-shot consumer call. The error
// wraps the gRPC status.
func queryPendingApprovals(ctx context.Context, ep discover.Endpoint) (*agentcoordpb.PendingApprovalsResult, error) {
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
