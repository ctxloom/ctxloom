package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Task definite-phoniness: coord.Coordinator gets an application-layer DRAIN
// state — BeginDrain stops every admission site from accepting NEW work while
// leaving already-admitted runs alone. These tests pin the four admission
// sites (AgentRun, StartOwnedRun, RunnerChannel's Hello for a runner with
// nothing already in flight, Serve) one at a time: normal admission still
// works before BeginDrain, every site refuses with ErrDraining afterward, a
// runner reconnecting to finish work it already holds is still admitted, and
// a turn already in flight when BeginDrain is called still reaches its
// ordinary terminal state rather than being cut off.

// TestBeginDrain_AgentRunRefusesNewWorkOnceDraining pins the AgentRun
// admission site: it admits normally before BeginDrain and refuses with the
// typed sentinel (a stated, actionable reason) after.
func TestBeginDrain_AgentRunRefusesNewWorkOnceDraining(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	require.False(t, c.Draining(), "a fresh coordinator is not draining")
	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "before drain", "", "")
	require.NoError(t, err, "admission must succeed while the coordinator is not draining")

	c.BeginDrain()
	require.True(t, c.Draining())

	_, err = c.AgentRun(context.Background(), ownerIdentity(), "worker", "after drain", "", "")
	require.Error(t, err, "admission must be refused once draining")
	assert.ErrorIs(t, err, ErrDraining, "the refusal must be the typed sentinel, not merely a similar-looking message")
	assert.Contains(t, err.Error(), "draining", "the reason must be stated, not just refused")
}

// TestBeginDrain_StartOwnedRunRefusesNewWorkOnceDraining pins the
// StartOwnedRun admission site the same way, and additionally proves the
// refusal happens BEFORE the runner starter is ever invoked — a drained
// coordinator must not spawn a runner process only to then find nowhere to
// send it.
func TestBeginDrain_StartOwnedRunRefusesNewWorkOnceDraining(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	// Baseline: admission succeeds normally before draining.
	baselineStarter, baselineStarted := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, baselineStarter, "hello before drain")
	require.NoError(t, err)
	require.True(t, *baselineStarted, "the baseline run must actually have launched")

	c.BeginDrain()

	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "hello after drain")
	require.Error(t, err, "admission must be refused once draining")
	assert.ErrorIs(t, err, ErrDraining)
	assert.Contains(t, err.Error(), "draining")
	assert.False(t, *started, "the starter must never be invoked once draining refuses admission")
}

// TestBeginDrain_RunnerChannelHelloRefusesFreshRunnerButAdmitsReconnect pins
// the RunnerChannel Hello admission site's two halves: a brand-new runner
// with nothing in flight (empty active_run_ids) is refused once draining —
// it represents capacity for new work that AgentRun/StartOwnedRun already
// refuse to ever assign — while the SAME credential reconnecting to a run it
// already holds (non-empty active_run_ids) is still admitted, because that
// is exactly "let in-flight turns finish".
func TestBeginDrain_RunnerChannelHelloRefusesFreshRunnerButAdmitsReconnect(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)

	// Baseline: a fresh runner Hello (no active runs) is admitted before
	// draining. Deliberately left connected rather than Shutdown here: an
	// explicit disconnect is a real RunnerChannel loss, which synthesizes
	// RunExited for this credential's owned run and revokes it — exactly
	// the confound this test must not introduce between its own dials.
	// t.Cleanup tears it down only once every assertion below is done.
	baseline, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], "", "mock", "test", nil)
	require.NoError(t, err, "a fresh runner Hello must be admitted while the coordinator is not draining")
	require.NotNil(t, baseline)
	t.Cleanup(func() { baseline.Shutdown(0, "") })

	c.BeginDrain()

	fresh, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], "", "mock", "test", nil)
	assert.Nil(t, fresh, "a refused Hello hands back no link")
	require.Error(t, err, "a fresh runner Hello must be refused once draining")
	assert.Contains(t, err.Error(), "draining", "the reason must be stated, not just refused")

	// A reconnect naming the run this credential already owns replaces the
	// baseline registration ("newest wins") rather than disconnecting it —
	// the same non-lossy path a genuine network-blip reconnect takes.
	reconnect, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
	require.NoError(t, err, "a runner reconnecting to a run it already holds must still be admitted while draining")
	require.NotNil(t, reconnect)
	t.Cleanup(func() { reconnect.Shutdown(0, "") })
}

// TestBeginDrain_ServeRefusesToStartFreshOnceDraining pins the Serve
// admission site: a coordinator that has been told to drain before it ever
// bound its listeners must refuse to stand them up, rather than silently
// beginning to accept runner/agent connections it will then have to refuse
// one at a time.
func TestBeginDrain_ServeRefusesToStartFreshOnceDraining(t *testing.T) {
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    newFakeSpawner(nil, nil),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	c.BeginDrain()

	err = c.Serve()
	require.Error(t, err, "Serve must refuse to bind fresh listeners once draining")
	assert.ErrorIs(t, err, ErrDraining)
	assert.Contains(t, err.Error(), "draining")
	assert.Nil(t, c.srv.Load(), "a refused Serve must leave no listener behind")
}

// TestBeginDrain_ServeStaysIdempotentOnceAlreadyServing proves BeginDrain
// does not turn an already-serving coordinator's idempotent no-op Serve()
// call into an error: existing callers that call Serve() more than once must
// keep observing the same "already serving" no-op, not a new failure mode
// introduced by draining.
func TestBeginDrain_ServeStaysIdempotentOnceAlreadyServing(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil) // Serve()'d once already

	c.BeginDrain()
	require.NoError(t, c.Serve(), "Serve on an already-serving coordinator stays a no-op even while draining")
}

// TestBeginDrain_InFlightTurnStillCompletes proves BeginDrain is admission
// refusal ONLY: a turn already accepted and in flight when BeginDrain is
// called must still reach its ordinary terminal state (roster idle) rather
// than being cut off, even though new admission is refused in the meantime.
func TestBeginDrain_InFlightTurnStillCompletes(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}},
	}, func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "in flight", "", "")
	require.NoError(t, err)

	// Reach the gate: the turn is genuinely mid-flight, parked on the
	// engine's turnGate, before draining is announced.
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		if len(sp.engines) == 0 {
			sp.mu.Unlock()
			return false // Launch's append (fake_test.go) has not run yet
		}
		e := sp.engines[0]
		sp.mu.Unlock()
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.texts) == 1
	}, conformanceWait, 5*time.Millisecond, "the in-flight turn must reach the engine before BeginDrain is called")

	c.BeginDrain()

	// New admission is refused while the in-flight turn is still parked.
	_, err = c.AgentRun(context.Background(), ownerIdentity(), "worker", "new work during drain", "", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDraining)

	close(gate) // release the in-flight turn

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"the in-flight turn must still complete normally (reach idle) once released, not be cut off by drain")
}
