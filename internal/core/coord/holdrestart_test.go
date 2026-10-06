package coord

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
)

// journaled decodes every runs.jsonl fact of kind, oldest first: what a
// restarted coordinator would replay.
func journaled[P any](t *testing.T, c *Coordinator, kind string) []P {
	t.Helper()
	var out []P
	for _, f := range readFacts(t, filepath.Join(c.stateDir, "runs.jsonl")) {
		if f.Kind != kind {
			continue
		}
		var p P
		require.NoError(t, f.decode(&p))
		out = append(out, p)
	}
	return out
}

// stepSignal is a holdStep seam that signals ch each time step is reached.
func stepSignal(step string, ch chan struct{}) func(string) {
	return func(s string) {
		if s == step {
			ch <- struct{}{}
		}
	}
}

// restart crashes f's coordinator and serves another over the same state dir,
// its clock standing down after the crashed one's. The runners outlive the
// crash and are NOT yet told to redial (redial does that), so anything
// asserted between the two is what adoption alone rebuilt.
func (f *holdFixture) restart(t *testing.T, was *fakeclock.Clock, down time.Duration, steps func(string)) *fakeclock.Clock {
	t.Helper()
	return f.restartTuned(t, was, down, func(c *Coordinator) {
		if steps != nil {
			c.holdStep = steps
		}
	})
}

// restartTuned is restart with the restarted coordinator adjusted by tune
// before it serves.
func (f *holdFixture) restartTuned(t *testing.T, was *fakeclock.Clock, down time.Duration, tune func(*Coordinator)) *fakeclock.Clock {
	t.Helper()
	crashCoordinator(f.c)
	clk := fakeclock.New()
	clk.Advance(was.Now().Sub(fakeclock.Epoch) + down)
	o := f.opts
	o.Clock, o.AfterFunc = clk.Now, clk.AfterFunc
	var findings report.Collector
	o.Reporter = &findings
	c, err := New(o)
	require.NoError(t, err)
	tune(c)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)
	f.c, f.findings = c, &findings
	return clk
}

// redial tells every runner its coordinator is back and waits for each live
// run's runner to re-Hello it.
func (f *holdFixture) redial(t *testing.T) {
	t.Helper()
	for i := range f.sp.chatCount() {
		f.sp.engineHome(i).Redial()
	}
	for _, harp := range []string{f.worker, f.sibling, f.stranger} {
		runID := currentRunID(f.c, harp)
		require.Eventually(t, func() bool { return f.c.runnerConnected(runID) }, conformanceWait, 10*time.Millisecond,
			"%s's runner never re-Helloed the restarted coordinator", harp)
	}
}

// awaitReplayed blocks until the i-th spawn's runner has had every event it
// emitted handled by f's current coordinator. A runner re-sends whatever the
// crashed coordinator never acked — a turn boundary among it when the crash
// fell between the turn's failure folding and its run reading idle — so a
// re-adopted run's state is its true one only once that replay has landed.
// A report waits for its own ack, which is cumulative and follows the replay
// on the same stream.
func (f *holdFixture) awaitReplayed(t *testing.T, i int) {
	t.Helper()
	require.NoError(t, f.sp.engineHome(i).Report(human(t), &agentcoordpb.Summary{Text: "caught up"}, nil),
		"spawn %d's runner never caught up with the restarted coordinator", i)
}

// TestHoldRestart_ARateLimitHoldSurvivesAdoption: a coordinator that dies
// mid-hold comes back with the hold in force — the same kind, credential,
// deadline and harps, its release timer re-armed, the roster showing it, an
// agent still refused — and at the deadline it releases itself, resuming
// every run it parked and running the mail held meanwhile.
func TestHoldRestart_ARateLimitHoldSurvivesAdoption(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	before := f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	f.send(t, f.sibling, "held work")

	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	after := f.c.CredentialHolds()
	require.Len(t, after, 1, "the hold is rebuilt from the journal")
	assert.Equal(t, before.Kind, after[0].Kind)
	assert.Equal(t, before.Source, after[0].Source)
	assert.Equal(t, before.Harps, after[0].Harps)
	assert.True(t, before.Until.Equal(after[0].Until), "got %v", after[0].Until)
	assert.Equal(t, 1, clk.Pending(), "its release timer is re-armed")
	assert.Equal(t, 1, f.findingsWith("still in force"), "the human is told again: %v", f.findings.All())

	f.redial(t)
	within(t, reasserted, "the worker's hold was never re-asserted")
	within(t, reasserted, "the sibling's hold was never re-asserted")
	for _, harp := range []string{f.worker, f.sibling} {
		got := f.holdOf(t, harp)
		require.NotNil(t, got, "%s is still held on the roster", harp)
		assert.Equal(t, string(agent.FailureRateLimited), got.Kind)
	}
	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.sibling)
	require.ErrorIs(t, err, ErrCredentialHeld, "an agent may not cut the backoff short after a restart either")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds(), "at the reset time the rebuilt hold releases itself")
	assertReleased(t, f.c, "backoff")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	awaitChatText(t, f.sp, 1, "held work")
}

// TestHoldRestart_AnOverloadHoldSurvivesAdoption: a run's own overload
// backoff is rebuilt for that run alone; its credential's other run is not
// parked, before the restart or after it.
func TestHoldRestart_AnOverloadHoldSurvivesAdoption(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, overloadHit+" do the work")
	f.awaitHold(t, f.worker)
	f.awaitParks(t, f.worker)

	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	holds := f.c.CredentialHolds()
	require.Len(t, holds, 1)
	assert.Equal(t, agent.FailureOverloaded, holds[0].Kind)
	assert.Equal(t, []string{f.worker}, holds[0].Harps)
	assert.Equal(t, 1, clk.Pending())

	f.redial(t)
	within(t, reasserted, "the overloaded run's hold was never re-asserted")
	assert.Nil(t, f.holdOf(t, f.sibling), "the credential's other run is not held")
	f.send(t, f.sibling, "free work")
	awaitChatText(t, f.sp, 1, "free work")

	clk.Advance(overloadBackoff)
	assert.Empty(t, f.c.CredentialHolds())
	assert.Equal(t, []string{f.worker}, resumedHarps(t, f.c))
}

// TestHoldRestart_ADeadlinePassedWhileDownReleasesOnAdopt: a hold whose
// deadline passed while the coordinator was down is released AT ADOPTION —
// journaled at once — but no resume is sent before a runner re-Hellos (there
// is none to send it to); each run's owed resume goes out once its runner is
// back, exactly once.
func TestHoldRestart_ADeadlinePassedWhileDownReleasesOnAdopt(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	resumes, reasserted := make(chan struct{}, 8), make(chan struct{}, 8)
	clk = f.restart(t, clk, limitResets.Sub(clk.Now())+time.Minute, func(s string) {
		switch s {
		case holdStepResume:
			resumes <- struct{}{}
		case holdStepReasserted:
			reasserted <- struct{}{}
		}
	})
	assert.Empty(t, f.c.CredentialHolds(), "the hold released itself at adoption")
	assertReleased(t, f.c, "backoff")
	assert.Empty(t, resumedHarps(t, f.c), "no runner is back, so no resume has landed")
	assert.Zero(t, clk.Pending(), "a released hold arms no timer")

	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	assert.Len(t, resumes, 2, "one resume per run, and none before its runner was back")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.worker)
	require.NoError(t, err)
	assert.False(t, newly, "the owed resume already reached the worker's runner")
	f.send(t, f.sibling, "after the outage")
	awaitChatText(t, f.sp, 1, "after the outage")
}

// TestHoldRestart_AHumanReleaseSurvivesARestart: a hold the human released
// is not rebuilt, and no timer is left armed for it.
func TestHoldRestart_AHumanReleaseSurvivesARestart(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	_, err := f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)

	clk = f.restart(t, clk, 0, nil)
	assert.Empty(t, f.c.CredentialHolds())
	assert.Zero(t, clk.Pending())
	f.redial(t)
	f.send(t, f.worker, "after the restart")
	awaitChatText(t, f.sp, 0, "after the restart")
}

// TestHoldRestart_AnOwedResumeIsDeliveredAfterARestart forces the crash
// between the human's release being journaled and its resumes reaching the
// runners: the restarted coordinator rebuilds no hold, and delivers each owed
// resume once its runner is back.
func TestHoldRestart_AnOwedResumeIsDeliveredAfterARestart(t *testing.T) {
	atResume, letGo := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	t.Cleanup(func() { release.Do(func() { close(letGo) }) }) // the crashed coordinator's resume must not stay stuck at the seam
	f, clk := newRateFixture(t, func(c *Coordinator) {
		c.holdStep = func(s string) {
			if s == holdStepResume {
				once.Do(func() { close(atResume) })
				<-letGo
			}
		}
	})
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	go func() { _, _ = f.c.ControlResume(context.Background(), humanInitiator(), f.sibling) }()
	within(t, atResume, "the human's release never reached its resumes")
	assertReleased(t, f.c, "human")

	reasserted := make(chan struct{}, 8)
	_ = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	assert.Empty(t, f.c.CredentialHolds(), "a released hold is not rebuilt")
	f.redial(t)
	within(t, reasserted, "an owed resume was never delivered")
	within(t, reasserted, "an owed resume was never delivered")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c), "each run's owed resume, once")
	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)
	assert.False(t, newly, "the owed resume already reached the sibling's runner")
}

// TestHoldRestart_ACrashBetweenParkAndPauseReassertsThePause forces the crash
// after a sibling's park is journaled but before its pause was sent: the
// restarted coordinator re-asserts the pause once the sibling's runner is
// back, so a run the journal calls held is held at its runner.
func TestHoldRestart_ACrashBetweenParkAndPauseReassertsThePause(t *testing.T) {
	parking, letPark := make(chan struct{}), make(chan struct{})
	var once, park sync.Once
	t.Cleanup(func() { park.Do(func() { close(letPark) }) })
	f, clk := newRateFixture(t, func(c *Coordinator) {
		c.holdStep = func(s string) {
			if s == holdStepParkSibling {
				once.Do(func() { close(parking) })
				<-letPark
			}
		}
	})
	f.send(t, f.worker, limitHit+" do the work")
	within(t, parking, "the hold never began parking the sibling")
	require.Equal(t, sortedCopy([]string{f.sibling, f.worker}), parkedHarps(journaled[holdParked](t, f.c, factHoldParked)),
		"the sibling's park is journaled before its pause is sent")

	reasserted := make(chan struct{}, 8)
	_ = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	f.redial(t)
	within(t, reasserted, "a held run's pause was never re-asserted")
	within(t, reasserted, "a held run's pause was never re-asserted")
	newly, err := f.c.ControlPause(human(t), humanInitiator(), f.sibling, "probe")
	require.NoError(t, err)
	assert.False(t, newly, "the restarted coordinator already paused the sibling at its runner")
}

// TestHoldRestart_AReadoptedRunKeepsItsCredential: a run re-adopted after a
// restart still carries its credential, so a later limit on its credential
// parks it too.
func TestHoldRestart_AReadoptedRunKeepsItsCredential(t *testing.T) {
	f, clk := newRateFixture(t)
	_ = f.restart(t, clk, 0, nil)
	f.redial(t)
	f.send(t, f.worker, limitHit+" after the restart")
	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN"}, hold.Source.EnvVars)
}

// TestHoldRestart_TheIdleReaperSparesHeldAndPausedRunsAfterARestart: once
// re-adopted, a held run and a human-paused run are still spared by the idle
// reaper.
func TestHoldRestart_TheIdleReaperSparesHeldAndPausedRunsAfterARestart(t *testing.T) {
	const idle = 5 * time.Minute
	f, clk := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	f.opts.IdleTimeout = idle
	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	f.redial(t)
	for range 3 {
		within(t, reasserted, "a held or paused run was never re-asserted")
	}
	for i := range 3 {
		f.awaitReplayed(t, i)
	}
	clk.Advance(idle + time.Minute)
	require.Len(t, f.c.CredentialHolds(), 1, "the hold still stands")
	f.c.reapIdleRuns()
	for _, h := range []string{f.worker, f.sibling, f.stranger} {
		assert.Equal(t, StateIdle, f.state(h), "a held or paused run is never idle-reaped, restart or not")
	}
}

// TestHoldRestart_ABoundaryReplayedBeforeItsRunnersHelloStillSettlesTheRun
// forces the crash after the worker's limited turn folded into its hold but
// before its run read idle, so its runner still holds that turn boundary
// unacked; and forces the restarted coordinator to receive the replayed
// boundary on the run channel before the runner channel's Hello has
// re-adopted the run. The boundary must still settle the run idle: dropped,
// the run reads executing until a turn it may never take.
func TestHoldRestart_ABoundaryReplayedBeforeItsRunnersHelloStillSettlesTheRun(t *testing.T) {
	var armed atomic.Bool
	atBoundary, letBoundary := make(chan struct{}), make(chan struct{})
	var reached, released sync.Once
	t.Cleanup(func() { released.Do(func() { close(letBoundary) }) }) // the crashed coordinator's boundary stays parked until the end
	f, clk := newRateFixture(t, func(c *Coordinator) {
		c.turnIdleHook = func(harp string) {
			if armed.Load() {
				reached.Do(func() { close(atBoundary) })
				<-letBoundary
			}
		}
	})
	armed.Store(true)
	f.send(t, f.worker, limitHit+" do the work")
	within(t, atBoundary, "the worker's limited turn never reached its boundary")

	letHello := make(chan struct{})
	var helloed sync.Once
	t.Cleanup(func() { helloed.Do(func() { close(letHello) }) })
	_ = f.restartTuned(t, clk, 0, func(c *Coordinator) {
		c.runnerHelloHook = func(string) { <-letHello }
	})
	for i := range f.sp.chatCount() {
		f.sp.engineHome(i).Redial()
	}
	awaitItemsDurable(t, f.c, f.sp, f.worker) // the replayed boundary has landed, its runner not yet re-Helloed
	helloed.Do(func() { close(letHello) })
	f.redial(t)
	f.awaitReplayed(t, 0)
	assert.Equal(t, StateIdle, f.state(f.worker), "a replayed turn boundary settles its run, whichever channel the runner got back first")
}

// TestHoldRestart_AHumanPauseSurvivesARestart: a human's pause is a hold of
// kind human with no deadline, journaled like any other: after a restart its
// run is still paused here (deliveries say so), no timer is armed for it, and
// the human's resume releases it.
func TestHoldRestart_AHumanPauseSurvivesARestart(t *testing.T) {
	f, clk := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	opened := journaled[holdOpened](t, f.c, factHoldOpened)
	require.Len(t, opened, 1)
	assert.Equal(t, HoldKindHuman, opened[0].Kind)
	assert.True(t, opened[0].Until.IsZero(), "a pause has no deadline")

	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	assert.Zero(t, clk.Pending(), "a pause arms no timer")
	assert.Empty(t, f.c.CredentialHolds(), "a pause is not a credential's hold")
	f.redial(t)
	within(t, reasserted, "the pause was never re-asserted")
	_, disposition, err := f.c.peerSend(newMessageID(), ownerIdentity(), f.stranger, KindMessage, "after the restart", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(deliveryPaused)
	assert.Equal(t, prose, disposition, "the restarted coordinator still knows the run is paused")

	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly, "the runner's gate was still up; this resume lifts it")
	awaitChatText(t, f.sp, 2, "after the restart")
	assertReleased(t, f.c, "human")
}

// TestHoldRestart_ANoDeadlineHoldHasNoTimerAcrossRestart (the
// credential_rejected shape): a hold the journal opened with no deadline is
// rebuilt with none — no timer before or after the restart, still in force
// past every backoff cap — and only the human releases it.
func TestHoldRestart_ANoDeadlineHoldHasNoTimerAcrossRestart(t *testing.T) {
	f, clk := newRateFixture(t)
	src := f.sp.credentialOf("worker").Source("mock")
	worker, sibling := f.runOf(t, f.worker), f.runOf(t, f.sibling)
	now := clk.Now()
	require.NoError(t, f.c.runs.Exec(func() ([]Fact, error) {
		return []Fact{
			factAt(factHoldOpened, now, holdOpened{ID: "hold-rejected", Key: src.Key, Scope: holdScopeCredential,
				Kind: string(agent.FailureCredentialRejected), Engine: "mock", Source: src}),
			factAt(factHoldParked, now, holdParked{Key: src.Key, RunID: worker, Harp: f.worker, Cause: "turn"}),
			factAt(factHoldParked, now, holdParked{Key: src.Key, RunID: sibling, Harp: f.sibling, Cause: "sibling"}),
		}, nil
	}))

	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	holds := f.c.CredentialHolds()
	require.Len(t, holds, 1)
	assert.Equal(t, agent.FailureCredentialRejected, holds[0].Kind)
	assert.True(t, holds[0].Until.IsZero(), "no deadline: %v", holds[0].Until)
	assert.Zero(t, clk.Pending(), "a hold with no deadline arms no timer")

	f.redial(t)
	within(t, reasserted, "the hold was never re-asserted")
	within(t, reasserted, "the hold was never re-asserted")
	got := f.holdOf(t, f.worker)
	require.NotNil(t, got)
	assert.True(t, got.Until.IsZero())

	clk.Advance(rateLimitCap + time.Hour)
	assert.Len(t, f.c.CredentialHolds(), 1, "nothing but the human releases it")
	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.sibling)
	require.ErrorIs(t, err, ErrCredentialHeld)
	_, err = f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)
	assert.Empty(t, f.c.CredentialHolds())
}

// TestHoldRestart_AHeldHarpIsNotRelaunchedUntilItsHoldReleases (no restart
// needed): a held run whose runner dies leaves its HARP held. Its leftover
// mail does not relaunch it into the spent limit, nor does a further send;
// both wait, and the hold's release relaunches the harp with its mail.
func TestHoldRestart_AHeldHarpIsNotRelaunchedUntilItsHoldReleases(t *testing.T) {
	deferred := make(chan struct{}, 8)
	f, clk := newRateFixture(t, func(c *Coordinator) {
		inner := c.holdStep
		c.holdStep = func(s string) {
			inner(s)
			if s == holdStepRelaunchHeld {
				deferred <- struct{}{}
			}
		}
	})
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	f.send(t, f.sibling, "held work")
	sibling := f.runOf(t, f.sibling)

	f.sp.killEngine(1)
	within(t, deferred, "the held harp's leftover mail relaunched it")
	require.True(t, f.c.runEnded(sibling))
	assert.Equal(t, 3, f.sp.chatCount(), "a held harp is not relaunched")
	ended := f.entry(f.sibling)
	assert.Equal(t, StateEnded, ended.State)
	require.NotNil(t, ended.Hold, "a held harp whose run ended still shows its hold on the roster")
	assert.Equal(t, string(agent.FailureRateLimited), ended.Hold.Kind)

	_, disposition, err := f.c.peerSend(newMessageID(), ownerIdentity(), f.sibling, KindMessage, "more held work", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(deliveryPaused)
	assert.Equal(t, prose, disposition, "a send to a held harp whose run ended waits for the release")
	within(t, deferred, "a send to the held harp relaunched it")
	assert.Equal(t, 3, f.sp.chatCount(), "a held harp is not relaunched")
	holds := f.c.CredentialHolds()
	require.Len(t, holds, 1)
	assert.Contains(t, holds[0].Harps, f.sibling, "the hold still covers the harp")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	awaitChatText(t, f.sp, 3, "held work")
}

// TestHoldsFold_ARunEndedWhileHeldLeavesItsHarpHeld pins the fold's rules: a
// member run that ends leaves the hold covering its harp (and owed nothing);
// the release makes each live member owed a resume, and the ack settles it. A
// hold with no deadline is in force like any other.
func TestHoldsFold_ARunEndedWhileHeldLeavesItsHarpHeld(t *testing.T) {
	f := newHoldsFold()
	at := fakeclock.Epoch
	apply := func(kind string, p any) { f.apply(factAt(kind, at, p)) }
	apply(factHoldOpened, holdOpened{ID: "h1", Key: "k", Scope: holdScopeCredential, Kind: string(agent.FailureCredentialRejected)})
	apply(factHoldParked, holdParked{Key: "k", RunID: "run-a", Harp: "harp-a", Cause: "turn"})
	apply(factHoldParked, holdParked{Key: "k", RunID: "run-b", Harp: "harp-b", Cause: "sibling"})
	require.Len(t, f.inForce(), 1, "a hold with no deadline is in force")

	apply(factRunEnded, runEnded{RunID: "run-a", Cause: CauseRunnerLoss})
	assert.Nil(t, f.holdOfRun("run-a"), "an ended run is in no hold")
	require.NotNil(t, f.holdOfHarp("harp-a"), "its harp is still held")
	assert.Equal(t, "k", f.holdOfRun("run-b").Key)

	apply(factHoldReleased, holdReleased{Key: "k", Cause: "human", By: UserSender})
	assert.Empty(t, f.inForce())
	assert.Nil(t, f.holdOfHarp("harp-a"))
	_, owedA := f.owedOf("run-a")
	assert.False(t, owedA, "nothing is owed to an ended run")
	key, owedB := f.owedOf("run-b")
	require.True(t, owedB)
	assert.Equal(t, "k", key)

	apply(factHoldResumed, holdResumed{Key: "k", RunID: "run-b", Harp: "harp-b"})
	_, owedB = f.owedOf("run-b")
	assert.False(t, owedB, "the ack settles the owed resume")
}

// TestHoldsFold_ReplayEqualsTheLiveFold: the holds a coordinator holds live
// are exactly the holds a replay of its journal rebuilds.
func TestHoldsFold_ReplayEqualsTheLiveFold(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	var live []holdRecord
	f.c.runs.View(func() { live = f.c.holdsF.inForce() })
	raw, err := os.ReadFile(filepath.Join(f.c.stateDir, "runs.jsonl"))
	require.NoError(t, err)
	cp := filepath.Join(t.TempDir(), "runs.jsonl")
	require.NoError(t, os.WriteFile(cp, raw, 0o600))
	replayed := newHoldsFold()
	s, err := openStore(cp, replayed)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Len(t, live, 2)
	assert.Equal(t, live, replayed.inForce())
}

// TestHoldRestart_AReauthenticatedEnvironmentReleasesTheRefusedCredentialsHold
// is the restart remedy: the human exports a fresh credential and restarts
// the session. The restarted coordinator re-resolves the hold's credential
// from ITS environment, finds it changed, and releases the hold itself
// (journaled "reauth"); the runs it parked are resumed as their runners come
// back, and the mail held meanwhile runs.
func TestHoldRestart_AReauthenticatedEnvironmentReleasesTheRefusedCredentialsHold(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, credRefused+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	f.send(t, f.sibling, "held work")

	f.opts.LookupEnv = envOf(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-fixture-fresh-token"})
	reasserted := make(chan struct{}, 8)
	f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	assert.Empty(t, f.c.CredentialHolds(), "a changed credential releases the refused one's hold at adoption")
	assertReleased(t, f.c, holdCauseReauth)
	assert.Equal(t, 1, f.findingsWith("re-authenticated"), "the human is told why: %v", f.findings.All())
	assert.Zero(t, f.findingsWith(refusedFinding), "no stale refusal notice: %v", f.findings.All())

	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	awaitChatText(t, f.sp, 1, "held work")
}

// TestHoldRestart_TheSameRefusedCredentialKeepsItsHold: a restart whose
// environment still carries the refused credential — or no longer carries one
// at all — cannot have fixed it, so the hold stays, with no timer, and the
// human is told again what to do.
func TestHoldRestart_TheSameRefusedCredentialKeepsItsHold(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"the same credential": {"CLAUDE_CODE_OAUTH_TOKEN": refusedToken},
		"no credential":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			f, clk := newRateFixture(t)
			f.send(t, f.worker, credRefused+" do the work")
			f.awaitHold(t, f.worker, f.sibling)
			f.awaitParks(t, f.worker, f.sibling)

			f.opts.LookupEnv = envOf(env)
			clk = f.restart(t, clk, 0, nil)
			holds := f.c.CredentialHolds()
			require.Len(t, holds, 1, "the hold stays")
			assert.Equal(t, agent.FailureCredentialRejected, holds[0].Kind)
			assert.Zero(t, clk.Pending())
			assert.Empty(t, journaled[holdReleased](t, f.c, factHoldReleased))
			assert.Equal(t, 1, f.findingsWith(refusedFinding), "the human is told again: %v", f.findings.All())
			assert.Equal(t, 1, f.findingsWith("export a fresh CLAUDE_CODE_OAUTH_TOKEN"), "with the remedy: %v", f.findings.All())
			assert.Zero(t, f.findingsWith(refusedToken), "never the value")
		})
	}
}

// secretsRefresher is Options.RefreshSecrets over the fixture's plain files:
// it records each file it was asked to rewrite and lays vals into it.
type secretsRefresher struct {
	mu       sync.Mutex
	files    []string
	released int
}

func (r *secretsRefresher) refresh(file string, vals map[string]string) (func(), error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	merged, err := sessions.DecodeSecrets(b)
	if err != nil {
		return nil, err
	}
	maps.Copy(merged, vals)
	if b, err = sessions.EncodeSecrets(merged); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = append(r.files, filepath.Base(file))
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.released++
	}, nil
}

// TestHoldRestart_ARestartRewritesAReadoptedRunsSecretsSoItsNextTurnUsesTheFreshCredential:
// worker and sibling run container-shaped — each runner reads its credential
// from a secrets file at every turn — while stranger runs host-shaped, with
// none. The refused credential parks worker and sibling. The human exports a
// fresh credential and restarts: the restarted coordinator rewrites each
// re-adopted run's secrets file with it (never the host-shaped one's) and
// re-journals its launch under the new fingerprint, so the hold releases and
// the very next refused-marker turn runs on the fresh credential — no new
// refusal, no new hold, no relaunch.
func TestHoldRestart_ARestartRewritesAReadoptedRunsSecretsSoItsNextTurnUsesTheFreshCredential(t *testing.T) {
	f, clk := newSecretsFixture(t, nil)
	f.send(t, f.worker, credRefused+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	assert.Equal(t, refusedToken, lastExecEnv(f.sp, 0)[tokenVar], "premise: the worker's turn read its secrets file")

	refresher := &secretsRefresher{}
	f.opts.RefreshSecrets = refresher.refresh
	f.opts.LookupEnv = envOf(map[string]string{tokenVar: freshToken})
	reasserted := make(chan struct{}, 8)
	f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	refresher.mu.Lock()
	assert.ElementsMatch(t, []string{f.worker + ".env", f.sibling + ".env"}, refresher.files,
		"each re-adopted container-shaped run's secrets are rewritten; the host-shaped one is not touched")
	refresher.mu.Unlock()
	fresh := engine.Credentials{Env: map[string]string{tokenVar: freshToken}}.Fingerprint()
	f.c.runs.View(func() {
		l, ok := f.c.holdsF.launchOf(currentRunID(f.c, f.worker))
		require.True(t, ok)
		assert.Equal(t, fresh, l.Fingerprint, "the launch is re-journaled under the credential it now carries")
	})
	assert.Empty(t, f.c.CredentialHolds())
	assertReleased(t, f.c, holdCauseReauth)

	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	f.send(t, f.worker, credRefused+" again, after the re-auth")
	awaitChatText(t, f.sp, 0, "again, after the re-auth")
	require.Eventually(t, func() bool { return f.state(f.worker) == StateIdle }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, freshToken, lastExecEnv(f.sp, 0)[tokenVar], "the re-adopted runner's next turn read the fresh credential")
	assert.Empty(t, f.c.CredentialHolds(), "no new refusal")
	assert.Len(t, journaled[holdOpened](t, f.c, factHoldOpened), 1, "no new hold opened")
	assert.Equal(t, 3, f.sp.chatCount(), "nothing was relaunched")
}

// newSecretsFixture is the hold fixture on a manual clock with worker and
// sibling container-shaped — each runner reads its credential from a secrets
// file at every turn — and stranger host-shaped; tune, when set, adjusts the
// coordinator before any child is spawned.
func newSecretsFixture(t *testing.T, tune func(*Coordinator)) (*holdFixture, *fakeclock.Clock) {
	t.Helper()
	secrets := t.TempDir()
	clk := fakeclock.New()
	f := newHoldFixtureOpts(t, rateFailure, func(o *Options) {
		o.Clock, o.AfterFunc = clk.Now, clk.AfterFunc
		sp := o.Spawner.(*fakeSpawner)
		sp.secretsDir, sp.secretAgents = secrets, map[string]bool{"worker": true, "sibling": true}
	}, tune)
	return f, clk
}

// TestHoldRestart_ARefusalReplayedAfterAReauthRestartOpensNoHold forces the
// crash after the worker's refused turn folded into its hold but before its
// boundary was acked, so its runner still holds that boundary and re-sends
// it to the coordinator restarted on a fresh credential. That refusal was of
// the credential the restart replaced, and the hold it opened is already
// released: re-folded against the run's refreshed launch it would open a hold
// on the fresh credential and tell the human their re-authentication was
// refused. It must fold into nothing.
func TestHoldRestart_ARefusalReplayedAfterAReauthRestartOpensNoHold(t *testing.T) {
	var armed atomic.Bool
	atBoundary, letBoundary := make(chan struct{}), make(chan struct{})
	var reached, released sync.Once
	t.Cleanup(func() { released.Do(func() { close(letBoundary) }) }) // the crashed coordinator's boundary stays parked until the end
	f, clk := newSecretsFixture(t, func(c *Coordinator) {
		c.turnIdleHook = func(string) {
			if armed.Load() {
				reached.Do(func() { close(atBoundary) })
				<-letBoundary
			}
		}
	})
	armed.Store(true)
	f.send(t, f.worker, credRefused+" do the work")
	within(t, atBoundary, "the worker's refused turn never reached its boundary")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	f.opts.RefreshSecrets = (&secretsRefresher{}).refresh
	f.opts.LookupEnv = envOf(map[string]string{tokenVar: freshToken})
	reasserted := make(chan struct{}, 8)
	f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	require.Empty(t, f.c.CredentialHolds(), "premise: the re-auth released the hold at adoption")
	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	f.awaitReplayed(t, 0)

	assert.Equal(t, StateIdle, f.state(f.worker), "the replayed boundary still settles the run")
	assert.Empty(t, f.c.CredentialHolds(), "no hold on the fresh credential")
	assert.Len(t, journaled[holdOpened](t, f.c, factHoldOpened), 1, "no new hold opened")
	assert.Zero(t, f.findingsWith(refusedFinding), "the human is not told the fresh credential was refused: %v", f.findings.All())
}

// lastExecEnv is the env the i-th child's latest turn started its engine with.
func lastExecEnv(sp *fakeSpawner, i int) map[string]string {
	sc := sp.chat(i)
	sc.Mu.Lock()
	defer sc.Mu.Unlock()
	if len(sc.Execs) == 0 {
		return nil
	}
	return sc.Execs[len(sc.Execs)-1].Env
}

// TestHoldRestart_AnAdoptionReleaseRelaunchesAnEndedMemberWithMail: a held
// member whose run ended before the restart (its runner lost) keeps its harp
// held, and its mail waits. When the restarted coordinator releases the hold
// at adoption — its deadline passed while it was down, or the credential was
// re-authenticated — that harp is relaunched with its waiting mail.
func TestHoldRestart_AnAdoptionReleaseRelaunchesAnEndedMemberWithMail(t *testing.T) {
	for name, tc := range map[string]struct {
		hit   string
		cause string
		down  time.Duration
		env   map[string]string
	}{
		"its backoff expired while down": {hit: limitHit, cause: "backoff", down: time.Hour},
		"the credential was re-authenticated": {hit: credRefused, cause: holdCauseReauth,
			env: map[string]string{tokenVar: freshToken}},
	} {
		t.Run(name, func(t *testing.T) {
			deferred := make(chan struct{}, 8)
			f, clk := newRateFixture(t, func(c *Coordinator) {
				inner := c.holdStep
				c.holdStep = func(s string) {
					inner(s)
					if s == holdStepRelaunchHeld {
						deferred <- struct{}{}
					}
				}
			})
			f.send(t, f.worker, tc.hit+" do the work")
			f.awaitHold(t, f.worker, f.sibling)
			f.awaitParks(t, f.worker, f.sibling)
			f.send(t, f.sibling, "held work")
			sibling := f.runOf(t, f.sibling)
			f.sp.killEngine(1)
			within(t, deferred, "the held harp's leftover mail relaunched it before the restart")
			require.True(t, f.c.runEnded(sibling), "premise: the member's run ended while held")
			require.Equal(t, 3, f.sp.chatCount())

			f.opts.LookupEnv = envOf(tc.env)
			f.restart(t, clk, tc.down, nil)
			assert.Empty(t, f.c.CredentialHolds(), "the adoption released the hold")
			assertReleased(t, f.c, tc.cause)
			awaitChatText(t, f.sp, 3, "held work")
		})
	}
}
