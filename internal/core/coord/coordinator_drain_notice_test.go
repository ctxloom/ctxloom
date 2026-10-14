package coord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// lockedBuffer is a goroutine-safe capture of the diagnostic stream: the
// drain warns from its own goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureDiagnostics routes the process's user-facing warnings (the stream
// termSink renders onto) into a buffer for the rest of the test.
func captureDiagnostics(t *testing.T) *lockedBuffer {
	t.Helper()
	var buf lockedBuffer
	t.Cleanup(clidiag.SetSink(&buf))
	return &buf
}

// TestBeginDrain_AnnouncesTheWaitOnceWhenARunIsStillInATurn: a shutdown drain
// that has a run mid-turn to wait for says so, once, on the user-facing
// warning stream — naming the run, the bound and how to stop waiting — so a
// session whose own run has ended does not sit silent for up to the bound.
func TestBeginDrain_AnnouncesTheWaitOnceWhenARunIsStillInATurn(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the turn holds the drain to its bound
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 300 * time.Millisecond

	harp := spawnGatedChild(t, sp, c)
	diag := captureDiagnostics(t)

	awaitDrain(t, c.BeginDrain())

	want := fmt.Sprintf(drainWaitNotice, shutdownPolicy().label, c.drainBound, 1, harp+" ("+drainPhaseInTurn+")")
	require.Contains(t, diag.String(), want, "the drain must announce what it waits for")
	assert.Equal(t, 1, strings.Count(diag.String(), want), "the wait is announced once, not per pass")
}

// TestBeginDrain_SaysNothingWhenNoRunIsPending: a drain with nothing in a turn
// — here a child between turns, which ends at drain start — settles without
// announcing a wait.
func TestBeginDrain_SaysNothingWhenNoRunIsPending(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	idle := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, idle) == StateIdle }, conformanceWait, 5*time.Millisecond)
	diag := captureDiagnostics(t)

	out := awaitDrain(t, c.BeginDrain())

	require.Equal(t, []string{idle}, out.Exited, "precondition: the idle child was tracked and ended at drain start")
	// The notice's fixed wording between the label and the bound, taken from
	// the constant so a reworded notice cannot leave this check vacuous.
	waiting, _, _ := strings.Cut(drainWaitNotice, "%s for")
	_, waiting, _ = strings.Cut(waiting, ": ")
	require.NotEmpty(t, strings.TrimSpace(waiting), "the notice's fixed wording must be found")
	assert.NotContains(t, diag.String(), waiting, "nothing pending, nothing announced")
}

// TestBeginDrain_NamesALaunchingRunAsLaunchingAndALaunchFailureSettlesAtOnce
// is the shutdown drain meeting a child whose agent_run was issued just before
// its parent's session ended: the run holds a slot, so the roster says
// executing, but its runner is still being stood up. The drain waits on it —
// a launch that succeeds runs its first turn — and its notice says it is
// launching, not "in a turn". When the launch then fails, the drain settles
// on that terminal at once, not at its bound.
func TestBeginDrain_NamesALaunchingRunAsLaunchingAndALaunchFailureSettlesAtOnce(t *testing.T) {
	resetStrictness(t)
	launchGate := make(chan struct{})
	sp := startRunSpawner(t, nil)
	sp.startGate = launchGate
	sp.startErr = errors.New("container isolation was requested but could not start")
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Hour // a drain that waits out its bound fails awaitDrain long before this

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateExecuting }, conformanceWait, time.Millisecond,
		"precondition: the child holds a slot and reads executing while its launch is held")
	diag := captureDiagnostics(t)

	d := c.BeginDrain()
	want := fmt.Sprintf(drainWaitNotice, shutdownPolicy().label, c.drainBound, 1, out.Harp+" ("+drainPhaseLaunching+")")
	require.Eventually(t, func() bool { return strings.Contains(diag.String(), want) }, conformanceWait, time.Millisecond,
		"the drain must name a run whose launch is in flight as launching")
	select {
	case <-d.Done():
		t.Fatal("the drain settled while the launch was still in flight")
	default:
	}

	close(launchGate) // the launch fails
	o := awaitDrain(t, d)
	assert.Equal(t, []string{out.Harp}, o.Exited, "the failed launch ended the run inside the bound")
	assert.Empty(t, o.Interrupted)
	assert.Equal(t, CauseLaunchFailed, currentRunCause(c, out.Harp))
}

// TestDrainPhase_ReadsTheLaunchAttemptsSettledSignal pins drainPhase on every
// shape of childRt.attached: open (the launch has not settled) is launching;
// nil (a spawn's markAttached settled it) and closed-but-kept (adoption closes
// it in place) are a run that is up, so in a turn; no attachment at all is
// not a launch this coordinator is running either.
func TestDrainPhase_ReadsTheLaunchAttemptsSettledSignal(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
	closed := make(chan struct{})
	close(closed)
	c.mu.Lock()
	c.attach["run-open"] = &childRt{runID: "run-open", attached: make(chan struct{})}
	c.attach["run-nil"] = &childRt{runID: "run-nil"}
	c.attach["run-closed"] = &childRt{runID: "run-closed", attached: closed}
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.attach, "run-open")
		delete(c.attach, "run-nil")
		delete(c.attach, "run-closed")
		c.mu.Unlock()
	})

	assert.Equal(t, drainPhaseLaunching, c.drainPhase("run-open"))
	assert.Equal(t, drainPhaseInTurn, c.drainPhase("run-nil"))
	assert.Equal(t, drainPhaseInTurn, c.drainPhase("run-closed"))
	assert.Equal(t, drainPhaseInTurn, c.drainPhase("run-unattached"))
}
