package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// The application-layer DRAIN's admission half: BeginDrain stops every
// admission site from accepting NEW work. These tests pin the four admission
// sites (AgentRun, StartOwnedRun, RunnerChannel's Hello for a runner with
// nothing already in flight, Serve) one at a time: normal admission still
// works before BeginDrain, every site refuses with ErrDraining afterward, and
// a runner reconnecting to finish work it already holds is still admitted.
// What the drain then does with the children it already has — the bounded
// wait, the force, the park — is coordinator_drain_bound_test.go's.

// TestBeginDrain_AgentRunRefusesNewWorkOnceDraining pins the AgentRun
// admission site: it admits normally before BeginDrain and refuses with the
// typed sentinel (a stated, actionable reason) after.
func TestBeginDrain_AgentRunRefusesNewWorkOnceDraining(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}},
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
	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{Launch: ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", agent.PermissionBypass)}, baselineStarter, "hello before drain")
	require.NoError(t, err)
	require.True(t, *baselineStarted, "the baseline run must actually have launched")

	c.BeginDrain()

	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{Launch: ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", agent.PermissionBypass)}, starter, "hello after drain")
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
	// The child's turn is held OPEN for the whole test: a child between
	// turns has nothing left to drain and BeginDrain ends it (revoking the
	// very credential these dials present), so only a run mid-turn can show
	// the Hello site's two halves.
	gate := make(chan struct{})
	defer close(gate)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}},
	}, func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)
	// The child's own runner is the baseline: it dialed home with this
	// credential and was admitted before draining. Waiting for it to be up
	// keeps the dials below from racing its registration for the same
	// credential (newest wins, and a superseded runner reconnects).
	require.NoError(t, c.awaitChildUp(context.Background(), out.Harp))

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
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    newFakeSpawner(nil, nil),
		OwnerHarp:  ownerIdentity().Harp,
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
