package coord

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// Launch-retry gate.
//
// terminateRun's leftover-mail tail relaunches a harp whose mailbox is
// non-empty. When the LAUNCH itself is what fails, that tail is a hot,
// unbounded, backoff-free loop: launch fails → failChild → terminateRun
// (CauseLaunchFailed) → the mail is still queued (the child never came up to
// drain it) → resume → launch fails → … Left unbounded this can spin at
// roughly two container launches per second for 49 minutes, and agent_stop
// reported success while it kept spinning underneath.
//
// These tests assert on PROGRESS: the number of launch attempts the spawner
// actually observes. A test that only checked "agent_stop returned nil" is
// exactly the blind spot that let the bug ship.

// failingLaunchSpawner is a Spawner whose engine launch always fails, counting
// attempts and honouring ctx cancellation — the stand-in for a container
// runtime that cannot start (image missing, shared-fs probe failing, daemon
// unreachable). It fails on BOTH launch halves so the legacy and the migrated
// (StartRun) route are covered by the same double.
type failingLaunchSpawner struct {
	*fakeSpawner
	attempts atomic.Int64
	// delay is how long one doomed launch takes, so a spinning loop is
	// observable at a sane rate instead of pegging a core.
	delay time.Duration
	// cancelled counts attempts that ended because their launch CONTEXT was
	// cancelled rather than by their own failure — the proof that a stop
	// reaches an in-flight launch, not merely the process it produced.
	cancelled atomic.Int64
	// resolves counts Resolve calls; resolveGate (when non-nil) blocks the
	// FIRST resume's Resolve — call 1 is the original agent_run, call 2 is
	// the relaunch. Holding the relaunch there parks the harp in exactly the
	// state an agent_stop can observe mid-hazard: the run record
	// says ENDED and the roster agrees, while a relaunch is already in
	// flight and about to mint a fresh run. Without this the window is
	// microseconds wide and the test is a coin flip.
	resolves    atomic.Int64
	resolveGate chan struct{}
}

func (s *failingLaunchSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
	if s.resolves.Add(1) == 2 && s.resolveGate != nil {
		select {
		case <-s.resolveGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.fakeSpawner.Resolve(ctx, agentName)
}

func newFailingLaunchSpawner(viaStartRun bool) *failingLaunchSpawner {
	return &failingLaunchSpawner{
		fakeSpawner: newFakeSpawner(map[string]fakeAgent{
			"worker": {perm: "bypass", runtime: "container", viaStartRun: viaStartRun},
		}, nil),
		delay: 10 * time.Millisecond,
	}
}

var errLaunchDoomed = errors.New("simulated container launch failure (shared-fs probe did not run)")

func (s *failingLaunchSpawner) doomedLaunch(ctx context.Context) error {
	s.attempts.Add(1)
	t := time.NewTimer(s.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return errLaunchDoomed
	case <-ctx.Done():
		s.cancelled.Add(1)
		return ctx.Err()
	}
}

func (s *failingLaunchSpawner) Launch(ctx context.Context, _ *SpawnPlan, _, _ string, _, _ map[string]string) (*operations.AgentChatLaunch, error) {
	return nil, s.doomedLaunch(ctx)
}

func (s *failingLaunchSpawner) StartEngine(ctx context.Context, _ *SpawnPlan, _, _ map[string]string) (*EngineSpawn, error) {
	return nil, s.doomedLaunch(ctx)
}

// spinUpRetryLoop drives a child into the failing-launch retry loop: spawn it
// (the first launch fails), then queue mail it can never drain, which is what
// arms the relaunch. Returns the child's harp once the loop is demonstrably
// running.
func spinUpRetryLoop(t *testing.T, c *Coordinator, sp *failingLaunchSpawner) string {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err, "agent_run itself is async: the LAUNCH failure surfaces on the mailbox, not here")

	// Mail for a child that is down is what arms the relaunch (§6a: an ended
	// child resumes with the message as its next turn).
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "ping", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sp.attempts.Load() >= 3 }, 10*time.Second, 10*time.Millisecond,
		"precondition: the launch-retry loop must actually be spinning for this test to mean anything")
	return out.Harp
}

// assertLaunchesStop is the (b) assertion in its load-bearing form: PROGRESS
// stops. It samples the launch-attempt count twice across a window many loop
// periods wide — never "did agent_stop return success", which is precisely
// what the incident's agent_stop did while the launcher span on.
func assertLaunchesStop(t *testing.T, sp *failingLaunchSpawner) {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // let one in-flight attempt drain
	settled := sp.attempts.Load()
	time.Sleep(600 * time.Millisecond)
	assert.Equal(t, settled, sp.attempts.Load(),
		"launch attempts kept increasing after agent_stop: the stop ended the run record but not the launch/retry loop")
}

// stopDuringArmedRelaunch reproduces the incident's exact window: the harp's
// run is already ENDED (the roster says so, and agent_stop will agree) while a
// relaunch is armed and in flight. agent_stop lands there, then the relaunch
// is released. Nothing about that sequence may leave a launch loop running.
func stopDuringArmedRelaunch(t *testing.T, viaStartRun bool) {
	t.Helper()
	resetStrictness(t)
	sp := newFailingLaunchSpawner(viaStartRun)
	sp.resolveGate = make(chan struct{})
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	// The first launch fails; the run ends as launch_failed.
	require.Eventually(t, func() bool { return sp.attempts.Load() >= 1 }, 10*time.Second, 10*time.Millisecond)

	// Queued mail arms the relaunch, which then parks in Resolve.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "ping", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.resolves.Load() >= 2 }, 10*time.Second, 10*time.Millisecond,
		"precondition: a relaunch must be in flight (parked in Resolve)")

	// The stop the operator issues. It observes an ENDED run — the success-
	// shaped answer the incident got.
	_, err = c.AgentStop(ownerIdentity(), out.Harp)
	require.NoError(t, err)

	// Release the relaunch that was already in flight behind the stop.
	close(sp.resolveGate)

	assertLaunchesStop(t, sp)
}

// TestAgentStop_StopsFailingLaunchRetryLoop is red-test (b): after agent_stop
// on a run whose launch is failing, the LAUNCH ATTEMPT COUNT stops
// increasing.
func TestAgentStop_StopsFailingLaunchRetryLoop(t *testing.T) {
	stopDuringArmedRelaunch(t, false)
}

// TestAgentStop_StopsFailingLaunchRetryLoop_StartRunPath is the same
// assertion for the MIGRATED (StartRun) spawn half — the route every
// production container child actually takes.
func TestAgentStop_StopsFailingLaunchRetryLoop_StartRunPath(t *testing.T) {
	stopDuringArmedRelaunch(t, true)
}

// TestAgentStop_StopsRunningLaunchRetryLoop is the simpler shape: the loop is
// freely spinning and the stop lands wherever it lands.
func TestAgentStop_StopsRunningLaunchRetryLoop(t *testing.T) {
	resetStrictness(t)
	sp := newFailingLaunchSpawner(false)
	c := newTestCoordinator(t, sp, nil)
	harp := spinUpRetryLoop(t, c, sp)

	_, err := c.AgentStop(ownerIdentity(), harp)
	require.NoError(t, err)

	assertLaunchesStop(t, sp)
}

// TestAgentStop_CancelsInFlightLaunch proves the stop reaches the launch
// CONTEXT, not just whatever process a completed launch produced: a stop
// issued while a launch is in flight aborts that launch through ctx, rather
// than letting it run to completion and re-arm the loop behind the stop.
func TestAgentStop_CancelsInFlightLaunch(t *testing.T) {
	resetStrictness(t)
	sp := newFailingLaunchSpawner(false)
	sp.delay = 3 * time.Second // a slow launch: the stop lands mid-flight
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.attempts.Load() >= 1 }, 10*time.Second, 10*time.Millisecond,
		"precondition: a launch must be in flight")

	_, err = c.AgentStop(ownerIdentity(), out.Harp)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sp.cancelled.Load() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"agent_stop did not cancel the in-flight launch's context: the launch ran to completion behind the stop")
}

// TestFailingLaunch_RetryIsBoundedAndGivesUpLoudly is defect (3): an
// unbounded, backoff-free retry is its own bug. A launch that has failed N
// times will not succeed on attempt N+1; the loop must bound itself, back
// off, and then fail LOUDLY to the parent's mailbox instead of spinning
// silently for an hour.
func TestFailingLaunch_RetryIsBoundedAndGivesUpLoudly(t *testing.T) {
	resetStrictness(t)
	sp := newFailingLaunchSpawner(false)
	c := newTestCoordinator(t, sp, nil)
	spinUpRetryLoop(t, c, sp)

	// Well past the point an unbounded loop would have run hundreds of times.
	time.Sleep(3 * time.Second)
	attempts := sp.attempts.Load()
	assert.LessOrEqual(t, attempts, int64(c.maxLaunchAttempts),
		"the launch retry is unbounded: %d attempts in 3s with no ceiling and no backoff", attempts)

	// And the give-up is LOUD: the parent learns, rather than the loop just
	// going quiet.
	msgs := recvKind(t, c, "error", 5*time.Second)
	require.NotEmpty(t, msgs, "a bounded retry that exhausts its attempts must tell the parent")
	assert.Contains(t, msgs[len(msgs)-1].Body, "giving up",
		"the terminal notice must say the launcher gave up, not just that one attempt failed")
}
