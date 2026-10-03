package coord

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
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
	crashCoordinator(f.c)
	clk := fakeclock.New()
	clk.Advance(was.Now().Sub(fakeclock.Epoch) + down)
	o := f.opts
	o.Clock, o.AfterFunc = clk.Now, clk.AfterFunc
	var findings report.Collector
	o.Reporter = &findings
	c, err := New(o)
	require.NoError(t, err)
	if steps != nil {
		c.holdStep = steps
	}
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
	clk.Advance(idle + time.Minute)
	require.Len(t, f.c.CredentialHolds(), 1, "the hold still stands")
	f.c.reapIdleRuns()
	for _, h := range []string{f.worker, f.sibling, f.stranger} {
		assert.Equal(t, StateIdle, f.state(h), "a held or paused run is never idle-reaped, restart or not")
	}
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
	_, disposition, err := f.c.peerSend(ownerIdentity(), f.stranger, KindMessage, "after the restart", nil, "")
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
	src := f.sp.credentialOf("worker")
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

	_, disposition, err := f.c.peerSend(ownerIdentity(), f.sibling, KindMessage, "more held work", nil, "")
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
