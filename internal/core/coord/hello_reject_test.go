package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Both handshakes carry a google.rpc.Status reject_reason precisely so a
// refusal is actionable — HelloAck.reject_reason is even annotated "set iff
// !accepted". These two tests pin that the reason reaches the caller instead
// of being thrown away for a bare "rejected".
//
// Why it matters beyond tidiness: the agent-plane handshake is retried
// forever by runChannelLoop, and a PERMANENT rejection (a run_id that was
// never issued to this credential) is indistinguishable from a coordinator
// that is merely down. Without the reason, an operator sees a channel that
// silently never attaches. On the runner plane the loss is worse still: the
// coordinator refused for a named reason and then sent an ack with the reason
// field left empty.

// TestRunChannelHello_RejectionCarriesTheCoordinatorsReason drives the
// agent-plane handshake directly (runChannelOnce is what the retry loop
// calls) with a run id the credential does not own.
func TestRunChannelHello_RejectionCarriesTheCoordinatorsReason(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	target, err := grpcTarget(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	// This credential is the session owner's, minted for the empty run id;
	// claiming a spawned run's id is exactly the ownership violation
	// RunChannel answers with a populated reject_reason.
	h := &Home{cfg: HomeConfig{RunID: "run-never-issued-to-me"}, ctx: ctx, cancel: cancel, conn: conn}

	err = h.runChannelOnce(agentcoordpb.NewCoordinatorServiceClient(conn))
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the handshake must be REFUSED, not left parked")
	assert.Contains(t, err.Error(), "was not issued to this credential",
		"the coordinator's own reject_reason must reach the caller, not be dropped for a bare %q", err.Error())
}

// TestDialRunner_RejectionCarriesTheCoordinatorsReason is the runner-plane
// half, driven through the public DialRunner seam. The coordinator rejects a
// RunnerHello whose active_run_ids name a run this credential was not issued.
func TestDialRunner_RejectionCarriesTheCoordinatorsReason(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	link, err := DialRunner(ctx, c.LoopbackURL(), token, "run-never-issued-to-me", "mock", "test", nil)
	if link != nil {
		t.Cleanup(func() { link.Shutdown(0, "") })
	}
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the handshake must be REFUSED, not left parked")
	assert.Contains(t, err.Error(), "was not issued to this credential",
		"the coordinator refused for a named reason; it must travel in reject_reason and reach the caller, not be dropped for a bare %q", err.Error())
}
