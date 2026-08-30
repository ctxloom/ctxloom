package coord

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// sudsy-patio: StartOwnedRun's issueStartRun failure path (owner_run.go:159-161)
// returns bare — `if err := c.issueStartRun(...); err != nil { return nil, err }`
// — unlike the two failure paths above it in the same function, which both
// call c.failChild explicitly. The filed concern was that this is "the ONE
// path where the coordinator's own internal cleanup never fires at all",
// stranding rt attached/executing in the roster forever.
//
// This test pins the OBSERVABLE contract regardless of which layer performs
// the cleanup: when issueStartRun fails, (a) the runner's kill func (rt.close)
// must be invoked, and (b) the run must not remain in the live roster
// (ListRuns with includeTerminal=false).
func TestStartOwnedRun_CleansUpOnIssueStartRunFailure(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		RunnerAwaitTimeout: 100 * time.Millisecond, // issueStartRun's awaitRunner budget
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-cleanup-leak"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	// A starter that launches "successfully" (kill func minted, killCalled
	// flips true when invoked) but NEVER dials home — so awaitRunner inside
	// issueStartRun is guaranteed to time out and issueStartRun returns a
	// non-nil error to StartOwnedRun.
	var killCalled atomic.Bool
	starter := func(_ context.Context, _ map[string]string) (func(), string, error) {
		return func() { killCalled.Store(true) }, "", nil
	}

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "hello")

	require.Error(t, err, "a runner that never dials home must fail the owner run")
	assert.Nil(t, outcome)

	// (a) The runtime's own kill func must have been invoked — this is what
	// releases the container/process. A leak here strands a live process.
	require.Eventually(t, func() bool { return killCalled.Load() }, 2*time.Second, 10*time.Millisecond,
		"StartOwnedRun's issueStartRun failure path must still invoke the runtime's close/kill func (rt.close) — "+
			"a bare return here would strand the runner")

	// (b) The roster must not retain the run as live/executing forever — the
	// observable symptom a user actually hits (agent stuck "executing").
	require.Eventually(t, func() bool {
		for _, r := range c.ListRuns(false, "").GetRuns() {
			if r.GetAgent().GetAgentId() == ownerHarp {
				return false // still present in the LIVE roster: leaked
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond,
		"a run whose issueStartRun failed must not remain in the live roster (includeTerminal=false) — "+
			"that is the symptom a user actually hits: an agent stuck 'executing' forever")
}
