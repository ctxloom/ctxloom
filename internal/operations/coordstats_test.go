package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestQueryCoordinatorSpoolStats_ReadsLiveCounters: a discovered endpoint
// answers with the counters its coordinator holds, every field intact.
func TestQueryCoordinatorSpoolStats_ReadsLiveCounters(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.stats = &agentcoordpb.SpoolStatsResult{
		Delivered: 1, Consumed: 2, Failed: 3, DoorbellDropped: 4, DoorbellRejected: 5,
	}
	startFakeCoordinator(t, home, "proj", f)
	eps, skipped := discover.List()
	require.Empty(t, skipped)
	require.Len(t, eps, 1)

	got, err := QueryCoordinatorSpoolStats(context.Background(), eps[0])
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.GetDelivered())
	assert.Equal(t, uint64(2), got.GetConsumed())
	assert.Equal(t, uint64(3), got.GetFailed())
	assert.Equal(t, uint64(4), got.GetDoorbellDropped())
	assert.Equal(t, uint64(5), got.GetDoorbellRejected())
}

// TestQueryCoordinatorSpoolStats_DeadEndpointErrs: an endpoint.json that
// outlived its coordinator (by design — the ports are kept for re-bind)
// must come back as an error naming the endpoint, never as zero counters
// that read like a healthy coordinator.
func TestQueryCoordinatorSpoolStats_DeadEndpointErrs(t *testing.T) {
	ep := discover.Endpoint{URL: discover.LoopbackURL(1), Cred: "stale"} // port 1: nothing listens
	_, err := QueryCoordinatorSpoolStats(context.Background(), ep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ep.URL)
}

// TestQueryCoordinatorSpoolStats_MalformedEndpointErrs mirrors
// watchConsumerFeed's contract: a URL with no host is refused before any
// dial, with a real message rather than a wrapped nil.
func TestQueryCoordinatorSpoolStats_MalformedEndpointErrs(t *testing.T) {
	_, err := QueryCoordinatorSpoolStats(context.Background(), discover.Endpoint{URL: "no-host-path"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "%!w")
	assert.Contains(t, err.Error(), "no host")
}
