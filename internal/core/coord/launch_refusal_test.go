package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// refusingLaunchSpawner is failingLaunchSpawner whose every launch is a
// DETERMINISTIC refusal: the agent's checkout path is occupied by a worktree
// it cannot adopt (launch.ErrWorkspaceUnadoptable). Nothing a retry does
// changes the occupant.
type refusingLaunchSpawner struct{ *failingLaunchSpawner }

func (s refusingLaunchSpawner) ResolveLaunch(ctx context.Context, _ *SpawnPlan, _ SpawnStart) (Resolved, error) {
	err := s.doomedLaunch(ctx)
	if errors.Is(err, errLaunchDoomed) {
		err = fmt.Errorf("worktree isolation: %q already exists as a git worktree but on branch %q: %w",
			"/wt/ctxloom-wt-worker", "refs/heads/someone/else", launch.ErrWorkspaceUnadoptable)
	}
	return Resolved{}, err
}

// TestRefusedLaunch_IsNotRetried: verprop's lame-wide-chant was relaunched
// four times over a checkout it could not adopt, each attempt failing the
// same way, before the bounded retry gave up. A deterministic refusal is
// tried ONCE: the leftover-mail relaunch does not re-arm on it, and the
// parent is told at once — loudly, with the refusal — that the launcher
// stopped and why, rather than that it "failed N times in a row".
func TestRefusedLaunch_IsNotRetried(t *testing.T) {
	resetStrictness(t)
	sp := newFailingLaunchSpawner(t)
	sp.firstGate = make(chan struct{})
	c := newTestCoordinator(t, refusingLaunchSpawner{sp}, nil)
	require.Greater(t, c.maxLaunchAttempts, 1, "precondition: the budget would allow a retry")

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.attempts.Load() >= 1 }, 10*time.Second, 10*time.Millisecond,
		"precondition: the original launch must be in flight (parked on firstGate)")
	// Mail queued behind the launch is what arms the relaunch.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "ping", nil, "")
	require.NoError(t, err)
	close(sp.firstGate)

	msgs := recvWhere(t, c, func(m Message) bool { return strings.Contains(m.Body, "giving up") }, 5*time.Second)
	require.NotEmpty(t, msgs, "the parent is told the launcher stopped")
	body := msgs[len(msgs)-1].Body
	assert.Contains(t, body, "someone/else", "the notice carries the refusal itself")
	assert.NotContains(t, body, "times in a row", "a refusal was not retried, so the notice does not say it was")

	// Past several backoffs of a retrying loop.
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int64(1), sp.attempts.Load(), "THE POINT: a deterministic refusal is tried once, not retried")
}
