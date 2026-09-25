package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// An owner run whose runner process is already DEAD must fail at once, not
// after the whole dial-home budget. issueStartRun races awaitRunner against
// rt.runnerWait (watchRunnerExit); on the owner path that waiter is the
// starter's OwnedRunner.Wait. Without it the parent learns nothing until
// defaultRunnerAwaitTimeout — `ctxloom run` silent for minutes over a runner
// that already exited, and the acceptance binary timing out on the
// container-died journey whenever the container-running barrier did not
// catch the death first.
//
// The budget here is generous and the bound tight, so the clock arm cannot be
// what ends the wait.
func TestStartOwnedRun_RunnerDeathBeforeDialHomeFailsFast(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	teeHome(t)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		RunnerAwaitTimeout: 10 * time.Second,
		OwnerHarp:          ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-runner-died"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	// The runner process exited cleanly the moment it was spawned, having
	// never dialed home — the `docker run` whose container never ran.
	starter := func(context.Context, map[string]string) (OwnedRunner, error) {
		return OwnedRunner{Kill: func() {}, Wait: func() error { return nil }}, nil
	}

	begin := time.Now()
	_, err = c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "", "/work", agent.PermissionBypass), false), starter, "hello")
	elapsed := time.Since(begin)

	require.Error(t, err, "a runner that died before dialing home must fail the owner run")
	assert.Less(t, elapsed, 2*time.Second,
		"a DEAD runner must end the dial-home wait on its death, not on the %s budget", 10*time.Second)
	assert.Contains(t, err.Error(), "exited before dialing home",
		"the failure must name the runner's death, not blame the clock")
}
