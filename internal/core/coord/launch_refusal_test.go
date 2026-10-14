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

// TestRefusedLaunch_TheRefusalIsForgottenOnceItNoLongerHolds: the refused
// flag answers only "did the LAST launch fail on a refusal a retry cannot
// change". A launch that attaches, an explicit new delivery (agent_send, the
// retry a human asks for once the checkout is fixed), and a later failure of
// an ordinary, retryable kind each mean it no longer holds — and a flag that
// outlived them would refuse every later relaunch of a healthy harp.
// MUTATIONS — keep the flag in noteLaunchAttached, keep it in
// clearLaunchGate, or make noteLaunchFailure only ever set it — each: red.
func TestRefusedLaunch_TheRefusalIsForgottenOnceItNoLongerHolds(t *testing.T) {
	refusal := fmt.Errorf("checkout occupied: %w", launch.ErrWorkspaceUnadoptable)
	for _, tc := range []struct {
		name  string
		clear func(c *Coordinator, harp string)
	}{
		{"a launch that attaches", func(c *Coordinator, harp string) { c.noteLaunchAttached(harp) }},
		{"an explicit new delivery", func(c *Coordinator, harp string) { c.clearLaunchGate(harp) }},
		{"a later retryable failure", func(c *Coordinator, harp string) {
			c.noteLaunchFailure(harp, errors.New("container daemon not answering"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			c := newTestCoordinator(t, newFakeSpawner(t, map[string]fakeAgent{"worker": {}}, nil), nil)
			require.Greater(t, c.maxLaunchAttempts, 2, "precondition: two failures leave budget to retry")
			const harp = "refused-harp"

			c.noteLaunchFailure(harp, refusal)
			require.True(t, c.launchRefused(harp), "precondition: the refusal is recorded")
			tc.clear(c, harp)

			assert.False(t, c.launchRefused(harp), "the refusal no longer holds")
			_, ok, exhausted := c.nextRelaunch(harp)
			assert.True(t, ok, "so the leftover-mail relaunch may try again")
			assert.False(t, exhausted)
		})
	}
}
