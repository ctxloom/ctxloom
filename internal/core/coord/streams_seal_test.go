package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRunnerChannel_RefusedOnceTheCoordinatorSeals FORCES the interleaving
// Coordinator.Close used to race: a stream handler that arrives after the
// close has begun. The seal is taken directly (the transition Close performs
// first), the runner dials in behind it, and the handler must refuse the slot
// rather than count itself into a join already in progress.
func TestRunnerChannel_RefusedOnceTheCoordinatorSeals(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	c.streams.seal()

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	link, err := DialRunner(ctx, termSink(), c.LoopbackURL(), token, "", "mock", "test", nil)
	if link != nil {
		t.Cleanup(link.Abort)
	}
	require.Error(t, err, "a handler arriving after the seal must be refused, not admitted")
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
}
