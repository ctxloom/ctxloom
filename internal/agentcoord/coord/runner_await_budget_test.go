package coord

import (
	"context"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Dial-home readiness budget.
//
// issueStartRun's actx/acancel := context.WithTimeout(ctx, c.runnerAwaitTimeout)
// (children.go) is the ONLY per-attempt deadline on a container launch's
// progress beyond agent_stop cancellation: StartEngine's isolation prepare
// (image build/pull) already ran and returned before this clock starts, and
// awaitRunner itself is a plain blocking receive on a channel the runner's
// RunnerChannel Hello closes — there is no polling loop to put a backoff on,
// only a single flat deadline to size correctly.
//
// A too-tight deadline here cannot be told apart, from the coordinator's
// side, from a genuinely broken launch: both end in "runner never dialed
// home". A real operator complaint was exactly this
// class of problem ("starting a container can take some time") — these
// tests pin the fix at both ends: the production default is measured
// generous, and the injected-Options.RunnerAwaitTimeout seam is proven to
// actually govern whether a slow-but-successful dial-home survives.

// TestRunnerAwaitTimeout_ProductionDefaultIsGenerous is the permanent guard
// against someone quietly tightening this back down: a container's dial-home
// can legitimately take much longer than a healthy launch's happy path under
// host contention (loaded daemon, DinD nesting, a busy bridge network), and
// widening this budget costs a HEALTHY launch nothing — awaitRunner returns
// the instant the runner's Hello arrives regardless of how large the
// deadline is; it is not a polling interval.
func TestRunnerAwaitTimeout_ProductionDefaultIsGenerous(t *testing.T) {
	assert.GreaterOrEqual(t, defaultRunnerAwaitTimeout, 5*time.Minute,
		"defaultRunnerAwaitTimeout (children.go) must budget minutes, not "+
			"seconds, for a just-spawned container runner to dial home — a "+
			"60s deadline is tight enough to mistake a slow-but-successful "+
			"start for a broken one")
}

// TestIssueStartRun_ToleratesSlowRunnerDialHomeWithinBudget proves the
// injected budget is not just a typed field nobody reads: a runner that
// dials home LATE — well past what a too-tight deadline would tolerate —
// must still be adopted as long as it lands inside c.runnerAwaitTimeout.
// This is issueStartRun's exact call shape (children.go), replayed directly
// against awaitRunner so the test runs in milliseconds instead of minutes.
func TestIssueStartRun_ToleratesSlowRunnerDialHomeWithinBudget(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless}}, nil)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		RunnerAwaitTimeout: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)
	credHash := hashToken(env[EnvCoordCred])

	// Simulate a slow-but-successful container start: nobody has dialed
	// home yet at 150ms — well past what a tight (e.g. old 60s-in-spirit-
	// but-tiny-in-test) deadline analog would tolerate in a scaled test —
	// but still inside the 300ms budget this coordinator was given.
	go func() {
		time.Sleep(150 * time.Millisecond)
		link, derr := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
		if derr == nil {
			t.Cleanup(link.cancel)
		}
	}()

	actx, acancel := context.WithTimeout(context.Background(), c.runnerAwaitTimeout)
	defer acancel()
	_, err = c.awaitRunner(actx, credHash)
	assert.NoError(t, err, "a runner that dials home within the configured budget must not be treated as a launch failure")
}

// TestIssueStartRun_TooTightBudgetFailsTheSameSlowDialHome is the negative
// twin: the identical 150ms-late dial-home, but a budget smaller than the
// delay. This is what "60s was too tight" looks like in miniature — the
// mechanism must actually be capable of failing a slow start, or the
// positive test above would be vacuous (an awaitRunner that never failed
// regardless of ctx wouldn't be proving the budget does anything).
func TestIssueStartRun_TooTightBudgetFailsTheSameSlowDialHome(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless}}, nil)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		RunnerAwaitTimeout: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)
	credHash := hashToken(env[EnvCoordCred])

	go func() {
		time.Sleep(150 * time.Millisecond)
		link, derr := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
		if derr == nil {
			t.Cleanup(link.cancel)
		}
	}()

	actx, acancel := context.WithTimeout(context.Background(), c.runnerAwaitTimeout)
	defer acancel()
	_, err = c.awaitRunner(actx, credHash)
	assert.Error(t, err, "a dial-home slower than the configured budget must still fail — otherwise the budget governs nothing")
}
