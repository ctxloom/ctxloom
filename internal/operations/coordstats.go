package operations

import (
	"context"
	"fmt"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
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
