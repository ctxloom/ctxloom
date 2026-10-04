package coord

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/liveness"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
)

// Prompt markers the rate-limit fixture's scripted engine fails on: a turn
// that ends on the shared limit, naming a reset at limitResets (or the later
// limitResetsLate), or naming none.
const (
	limitHit        = "limit-hit"
	limitHitLate    = "limit-late"
	limitHitNoReset = "limit-noreset"
	// overloadHit: a turn the engine turns away because its server is at
	// capacity (claude's 529) — no reset time, nothing about the credential.
	overloadHit = "server-overloaded"
)

var (
	limitResets     = fakeclock.Epoch.Add(10 * time.Minute)
	limitResetsLate = fakeclock.Epoch.Add(20 * time.Minute)
)

// rateFailure is the fixture's engine: the limit markers end the turn on the
// limit, the overload marker on the server's capacity.
func rateFailure(prompt string) *agent.TurnFailure {
	switch {
	case strings.Contains(prompt, overloadHit):
		return &agent.TurnFailure{Kind: agent.FailureOverloaded}
	case strings.Contains(prompt, limitHitLate):
		return &agent.TurnFailure{Kind: agent.FailureRateLimited, ResetsAt: limitResetsLate}
	case strings.Contains(prompt, limitHitNoReset):
		return &agent.TurnFailure{Kind: agent.FailureRateLimited}
	case strings.Contains(prompt, limitHit):
		return &agent.TurnFailure{Kind: agent.FailureRateLimited, ResetsAt: limitResets}
	}
	return nil
}

// holdFixture is three children: worker and sibling on one credential,
// stranger on another, each idle after its briefing, behind a coordinator
// whose findings are collected.
type holdFixture struct {
	c                         *Coordinator
	sp                        *fakeSpawner
	findings                  *report.Collector
	worker, sibling, stranger string
	// opts are the coordinator's Options, kept so a restart (holdrestart_test.go)
	// serves the same state dir and spawner.
	opts Options
	// folded receives once per turn failure its hold has taken in
	// (newRateFixture only).
	folded chan struct{}
}

// newHoldFixtureOpts is the hold fixture with its engine's failures decided
// by failed, its Options adjusted by opts, and the coordinator adjusted by
// tune before any child is spawned (both may be nil).
func newHoldFixtureOpts(t *testing.T, failed func(prompt string) *agent.TurnFailure, opts func(*Options), tune func(*Coordinator)) *holdFixture {
	t.Helper()
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	for _, name := range []string{"sibling", "stranger"} {
		sp.agents[name] = fakeAgent{perm: "bypass", runtime: launch.RuntimeRootless}
	}
	shared := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("mock")
	other := engine.Credentials{Env: map[string]string{"ANTHROPIC_API_KEY": "v"}}.Source("mock")
	sp.credentialFor = func(agentName string) engine.CredentialSource {
		if agentName == "stranger" {
			return other
		}
		return shared
	}
	sp.nextChat = func() *scriptedChat { return &scriptedChat{Failed: failed} }
	var findings report.Collector
	o := Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		Reporter:   &findings,
		OwnerHarp:  ownerIdentity().Harp,
	}
	if opts != nil {
		opts(&o)
	}
	c, err := New(o)
	require.NoError(t, err)
	if tune != nil {
		tune(c)
	}
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)
	f := &holdFixture{c: c, sp: sp, findings: &findings, opts: o}
	f.worker = f.spawnIdle(t, "worker", 0)
	f.sibling = f.spawnIdle(t, "sibling", 1)
	f.stranger = f.spawnIdle(t, "stranger", 2)
	return f
}

// newRateFixture is the hold fixture on a manual clock: the coordinator's
// command time and its hold timers are clk's, so a backoff elapses only when
// the test advances it. tune, when set, adjusts the coordinator further.
func newRateFixture(t *testing.T, tune ...func(*Coordinator)) (*holdFixture, *fakeclock.Clock) {
	t.Helper()
	clk := fakeclock.New()
	folded := make(chan struct{}, 64) // beyond any test's failures: a full buffer would stall the coordinator
	f := newHoldFixtureOpts(t, rateFailure, func(o *Options) { o.Clock, o.AfterFunc = clk.Now, clk.AfterFunc }, func(c *Coordinator) {
		c.holdStep = func(step string) {
			if step == holdStepTurnFolded {
				folded <- struct{}{}
			}
		}
		for _, fn := range tune {
			fn(c)
		}
	})
	f.folded = folded
	return f, clk
}

// spawnIdle starts agentName as the i-th spawn and waits for its briefing's
// turn boundary.
func (f *holdFixture) spawnIdle(t *testing.T, agentName string, i int) string {
	t.Helper()
	out, err := f.c.AgentRun(context.Background(), ownerIdentity(), agentName, "first task", "", "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, f.c.awaitChildUp(ctx, out.Harp))
	awaitChatText(t, f.sp, i, "first task")
	require.Eventually(t, func() bool { return f.state(out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"%s never reached its first turn boundary", agentName)
	return out.Harp
}

func (f *holdFixture) entry(harp string) RosterEntry {
	for _, e := range f.c.Roster(ownerIdentity()) {
		if e.Harp == harp {
			return e
		}
	}
	return RosterEntry{}
}

func (f *holdFixture) state(harp string) string { return f.entry(harp).State }

func (f *holdFixture) send(t *testing.T, harp, body string) {
	t.Helper()
	_, _, err := f.c.peerSend(newMessageID(), ownerIdentity(), harp, KindMessage, body, nil, "")
	require.NoError(t, err)
}

// awaitHold waits for the one hold to hold exactly harps.
func (f *holdFixture) awaitHold(t *testing.T, harps ...string) CredentialHold {
	t.Helper()
	var got []CredentialHold
	require.Eventually(t, func() bool {
		got = f.c.CredentialHolds()
		return len(got) == 1 && assert.ObjectsAreEqual(sortedCopy(harps), got[0].Harps)
	}, conformanceWait, 10*time.Millisecond, "never one hold over %v (saw %+v)", harps, got)
	return got[0]
}

// awaitParks waits for the journal to have parked exactly harps on turn
// failures' holds (hold.parked; a pause's own park is not one).
func (f *holdFixture) awaitParks(t *testing.T, harps ...string) {
	t.Helper()
	want := sortedCopy(harps)
	var got []string
	require.Eventually(t, func() bool {
		got = parkedHarps(journaled[holdParked](t, f.c, factHoldParked))
		return assert.ObjectsAreEqual(want, got)
	}, conformanceWait, 10*time.Millisecond, "no hold parked %v (saw %v)", want, got)
}

// assertOpened pins what every hold the journal opened carries: the hold's
// kind and scope, which a reader cannot recover from its key.
func assertOpened(t *testing.T, c *Coordinator, kind agent.FailureKind, scope holdScope) []holdOpened {
	t.Helper()
	opened := journaled[holdOpened](t, c, factHoldOpened)
	require.NotEmpty(t, opened)
	for _, o := range opened {
		assert.Equal(t, string(kind), o.Kind, "%+v", o)
		assert.Equal(t, scope, o.Scope, "%+v", o)
	}
	return opened
}

// assertReleased pins that every release the journal holds was for cause.
func assertReleased(t *testing.T, c *Coordinator, cause string) {
	t.Helper()
	released := journaled[holdReleased](t, c, factHoldReleased)
	require.NotEmpty(t, released)
	for _, r := range released {
		assert.Equal(t, cause, r.Cause, "%+v", r)
	}
}

func parkedHarps(ps []holdParked) []string {
	var out []string
	for _, p := range ps {
		if p.Cause != "pause" {
			out = append(out, p.Harp)
		}
	}
	return sortedCopy(out)
}

// resumedHarps is every harp whose runner acked a released hold's resume.
func resumedHarps(t *testing.T, c *Coordinator) []string {
	var out []string
	for _, r := range journaled[holdResumed](t, c, factHoldResumed) {
		out = append(out, r.Harp)
	}
	return sortedCopy(out)
}

// awaitFolds waits until n turn failures have been taken into their holds. A
// failure that joins a hold changes nothing observable, so this is the only
// sign it landed.
func (f *holdFixture) awaitFolds(t *testing.T, n int) {
	t.Helper()
	for range n {
		within(t, f.folded, "a turn's failure was never folded into its hold")
	}
}

func (f *holdFixture) findingsWith(s string) int {
	n := 0
	for _, x := range f.findings.All() {
		if strings.Contains(x.Text, s) {
			n++
		}
	}
	return n
}

// gate holds the i-th child's next turn inside its engine until the returned
// function is called.
func (f *holdFixture) gate(i int) (release func()) {
	g := make(chan struct{})
	sc := f.sp.chat(i)
	sc.Mu.Lock()
	sc.Gate = g
	sc.Mu.Unlock()
	return func() { close(g) }
}

// runOf is harp's live run id.
func (f *holdFixture) runOf(t *testing.T, harp string) string {
	t.Helper()
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	for id, rt := range f.c.attach {
		if rt.harp == harp {
			return id
		}
	}
	t.Fatalf("%s has no live run", harp)
	return ""
}

// within fails the test unless ch yields within conformanceWait — a step that
// never comes must fail the test, not hang it.
func within(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(conformanceWait):
		t.Fatal(msg)
	}
}

func human(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	t.Cleanup(cancel)
	return ctx
}

// TestRateHold_OneLimitParksTheCredentialsRunsUntilItResets is the row's
// SETTLES: one child's turn ends on its usage limit, so every run sharing the
// credential is parked — no new turns, mail held — while a run on another
// credential carries on; an agent may not release it; at the engine's reset
// time the hold releases ITSELF, every held run resumes, held mail runs, and
// park and resume are journaled. No human acts.
func TestRateHold_OneLimitParksTheCredentialsRunsUntilItResets(t *testing.T) {
	f, clk := newRateFixture(t)

	f.send(t, f.worker, limitHit+" do the work")
	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, agent.FailureRateLimited, hold.Kind)
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN"}, hold.Source.EnvVars)
	assert.True(t, limitResets.Equal(hold.Until), "the hold waits for the engine's reset time: %v", hold.Until)
	f.awaitParks(t, f.worker, f.sibling)
	assert.Equal(t, 1, f.findingsWith("rate limit"), "ONE finding for the hold: %v", f.findings.All())
	opened := assertOpened(t, f.c, agent.FailureRateLimited, holdScopeCredential)
	require.Len(t, opened, 1)
	assert.Equal(t, hold.Source.Key, opened[0].Key, "a credential hold is keyed by its credential")
	assert.True(t, limitResets.Equal(opened[0].Until), "the journal names the deadline: %v", opened[0].Until)

	f.send(t, f.sibling, "held work")
	f.send(t, f.stranger, "free work")
	awaitChatText(t, f.sp, 2, "free work")
	require.Never(t, func() bool { return countChatText(f.sp, 1, "held work") > 0 },
		300*time.Millisecond, 10*time.Millisecond, "a parked sibling must take no new turn")

	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.sibling)
	require.ErrorIs(t, err, ErrCredentialHeld, "an agent may not cut the backoff short")

	clk.Advance(limitResets.Sub(clk.Now()) - time.Second)
	assert.Len(t, f.c.CredentialHolds(), 1, "a second early, the hold stands")

	clk.Advance(time.Second) // the release runs on this goroutine, inside Advance
	assert.Empty(t, f.c.CredentialHolds(), "at the reset time the hold releases itself")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	assertReleased(t, f.c, "backoff")
	awaitChatText(t, f.sp, 1, "held work")
	f.send(t, f.worker, "work after the reset")
	awaitChatText(t, f.sp, 0, "work after the reset")
}

// A limit with no reset time named waits the bounded default.
func TestRateHold_NoResetTimeWaitsTheDefault(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHitNoReset+" do the work")
	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.True(t, clk.Now().Add(rateLimitDefault).Equal(hold.Until), "got %v", hold.Until)
}

// TestRateHold_ASiblingMidTurnJoinsTheHoldOnce forces the race: the sibling's
// turn is INSIDE its engine when the park is applied. The pause gate holds only
// the hand-off, so that turn runs on, meets the same limit, and parks itself —
// joining the one hold, with no second finding. The release still frees it.
func TestRateHold_ASiblingMidTurnJoinsTheHoldOnce(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, limitHit+" slow work")
	awaitChatText(t, f.sp, 1, "slow work") // the sibling's turn is inside its engine
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling) // the park reached the sibling mid-turn
	release()
	f.awaitFolds(t, 2)
	f.awaitParks(t, f.worker, f.sibling)
	f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, 1, f.findingsWith("rate limit"), "the sibling's own failure joins the hold; no second finding: %v", f.findings.All())

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	f.send(t, f.sibling, "after the race")
	awaitChatText(t, f.sp, 1, "after the race")
}

// TestRateHold_ALaterLimitExtendsTheWait: a sibling whose in-flight turn ends
// on the limit with a LATER reset moves the shared deadline out; the first
// deadline passes without a release.
func TestRateHold_ALaterLimitExtendsTheWait(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, limitHitLate+" slow work")
	awaitChatText(t, f.sp, 1, "slow work")
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	release()
	f.awaitFolds(t, 2)
	holds := f.c.CredentialHolds()
	require.Len(t, holds, 1)
	require.True(t, limitResetsLate.Equal(holds[0].Until), "the later reset must extend the hold: %v", holds[0].Until)

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Len(t, f.c.CredentialHolds(), 1, "the first reset time no longer releases")
	clk.Advance(limitResetsLate.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
}

// The converse order: an EARLIER reset arriving after a later one never
// shortens the shared wait.
func TestRateHold_AnEarlierLimitNeverShortensTheWait(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, limitHit+" slow work")
	awaitChatText(t, f.sp, 1, "slow work")
	f.send(t, f.worker, limitHitLate+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	release()
	f.awaitFolds(t, 2)

	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.True(t, limitResetsLate.Equal(hold.Until), "got %v", hold.Until)
	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Len(t, f.c.CredentialHolds(), 1, "the earlier reset does not release")
}

// TestRateHold_ATimerTooLateToStopReleasesNothing: Go's Timer.Stop can lose
// to a callback already on its way. Modelled by a stop that never stops: the
// superseded deadline's callback still fires, and must not release a hold
// whose deadline has since moved.
func TestRateHold_ATimerTooLateToStopReleasesNothing(t *testing.T) {
	f, clk := newRateFixture(t, func(c *Coordinator) {
		inner := c.afterFunc
		c.afterFunc = func(d time.Duration, fn func()) func() bool {
			inner(d, fn)
			return func() bool { return false }
		}
	})
	release := f.gate(1)
	f.send(t, f.sibling, limitHitLate+" slow work")
	awaitChatText(t, f.sp, 1, "slow work")
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	release()
	f.awaitFolds(t, 2)
	h := f.c.CredentialHolds()
	require.Len(t, h, 1)
	require.True(t, limitResetsLate.Equal(h[0].Until))

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Len(t, f.c.CredentialHolds(), 1, "a superseded timer must not release the hold")
}

// The human may cut a backoff short: their resume releases the timed hold
// and disarms its timer.
func TestRateHold_TheHumanMayReleaseEarly(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	require.Equal(t, 1, clk.Pending())

	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)
	assert.True(t, newly)
	assert.Empty(t, f.c.CredentialHolds())
	assert.Zero(t, clk.Pending(), "the released hold's timer is disarmed")
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c))
	assertReleased(t, f.c, "human")
	f.send(t, f.sibling, "after the human")
	awaitChatText(t, f.sp, 1, "after the human")
}

// TestRateHold_AHumansPauseIsNotTheHolds: a sibling the HUMAN paused before
// the limit was hit is not taken into the hold, so the hold's release does not
// undo the human's own pause.
func TestRateHold_AHumansPauseIsNotTheHolds(t *testing.T) {
	f, clk := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.sibling, "reviewing")
	require.NoError(t, err)

	f.send(t, f.worker, limitHit+" do the work")
	f.awaitParks(t, f.worker)
	f.awaitHold(t, f.worker)

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	f.send(t, f.sibling, "held by the human")
	f.send(t, f.worker, "work after the reset")
	awaitChatText(t, f.sp, 0, "work after the reset")
	require.Never(t, func() bool { return countChatText(f.sp, 1, "held by the human") > 0 },
		300*time.Millisecond, 10*time.Millisecond, "the hold's release must leave the human's pause in place")
}

// TestRateHold_AHumanPausedRunsLimitStillParksItsSiblings forces a run the
// human paused WHILE its turn was inside the engine: the turn runs on and
// meets the limit. The limit is as spent for its siblings, so they are parked
// — but the paused run stays the human's: not in the hold, and still paused
// after the hold releases.
func TestRateHold_AHumanPausedRunsLimitStillParksItsSiblings(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(0)
	f.send(t, f.worker, limitHit+" slow work")
	awaitChatText(t, f.sp, 0, "slow work")
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.worker, "reviewing")
	require.NoError(t, err)
	release()

	f.awaitParks(t, f.sibling)
	f.awaitHold(t, f.sibling)
	assert.Equal(t, 1, f.findingsWith("rate limit"), "the human is told: %v", f.findings.All())

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	f.send(t, f.sibling, "sibling after the reset")
	f.send(t, f.worker, "held by the human")
	awaitChatText(t, f.sp, 1, "sibling after the reset")
	require.Never(t, func() bool { return countChatText(f.sp, 0, "held by the human") > 0 },
		300*time.Millisecond, 10*time.Millisecond, "the hold's release must leave the human's pause in place")
}

// A human-paused run alone on its credential meets the limit mid-turn: the
// human is told, no hold stays in force, and no timer is left armed for it.
func TestRateHold_NothingToParkLeavesNoTimer(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(2)
	f.send(t, f.stranger, limitHit+" slow work")
	awaitChatText(t, f.sp, 2, "slow work")
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	release()
	f.awaitFolds(t, 1)
	assert.Equal(t, 1, f.findingsWith("rate limit"), "%v", f.findings.All())
	assert.Empty(t, f.c.CredentialHolds())
	assert.Zero(t, clk.Pending(), "a hold that parks nothing arms no timer")
}

// TestRateHold_AReleaseNeverOvertakesTheHoldsOwnPark forces the backoff
// expiring WHILE the hold is still pausing a sibling: the sibling's pause is
// held at the step seam, the deadline passes and the release begins, and only
// then does the pause go out. A release that resumed the sibling first would
// leave the late pause standing, in no hold, forever — so the release waits
// for the parking to finish, and the sibling ends up running.
func TestRateHold_AReleaseNeverOvertakesTheHoldsOwnPark(t *testing.T) {
	parking := make(chan struct{})
	letPark := make(chan struct{})
	releasing := make(chan struct{})
	var once sync.Once
	park := func() { once.Do(func() { close(letPark) }) }
	t.Cleanup(park) // a failed test must not leave the hold's parking stuck at the seam
	f, clk := newRateFixture(t, func(c *Coordinator) {
		c.holdStep = func(step string) {
			switch step {
			case holdStepParkSibling:
				close(parking)
				<-letPark
			case holdStepReleaseWait:
				close(releasing)
			}
		}
	})
	f.send(t, f.worker, limitHit+" do the work")
	within(t, parking, "the hold never began parking the sibling") // registered; its pause has not gone out

	advanced := make(chan struct{})
	go func() { clk.Advance(limitResets.Sub(fakeclock.Epoch)); close(advanced) }()
	within(t, releasing, "the backoff's release never began")
	// The invariant itself: while the hold is still parking, the release may
	// not take it out of force. Nothing can change it here in correct code;
	// a release that did not wait would already have.
	require.Never(t, func() bool { return len(f.c.CredentialHolds()) == 0 }, 300*time.Millisecond, 10*time.Millisecond,
		"the backoff released the hold before its parking was done")
	park()
	within(t, advanced, "the release never finished")

	assert.Empty(t, f.c.CredentialHolds())
	f.send(t, f.sibling, "after the reset")
	awaitChatText(t, f.sp, 1, "after the reset")
}

// TestRateHold_TheBackoffAndTheHumanReleaseItOnce forces both releases onto
// one hold: the deadline passes and the human resumes while the hold is still
// parking, so both wait on it; the backoff's release goes first and finishes,
// and the human's then finds the hold already released. Each run is resumed —
// and journaled — once.
func TestRateHold_TheBackoffAndTheHumanReleaseItOnce(t *testing.T) {
	parking, letPark := make(chan struct{}), make(chan struct{})
	timerWaiting, humanWaiting, advanced := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	park := func() { once.Do(func() { close(letPark) }) }
	t.Cleanup(park)
	var waits int
	f, clk := newRateFixture(t, func(c *Coordinator) {
		c.holdStep = func(step string) {
			switch step {
			case holdStepParkSibling:
				close(parking)
				<-letPark
			case holdStepReleaseWait:
				waits++ // the two calls are ordered by the test: the timer's first
				if waits == 1 {
					close(timerWaiting)
					return
				}
				close(humanWaiting)
				<-advanced // the human's release waits until the backoff's is done
			}
		}
	})
	f.send(t, f.worker, limitHit+" do the work")
	within(t, parking, "the hold never began parking the sibling")

	go func() { clk.Advance(limitResets.Sub(fakeclock.Epoch)); close(advanced) }()
	within(t, timerWaiting, "the backoff's release never began")
	resumed := make(chan error, 1)
	go func() {
		_, err := f.c.ControlResume(context.Background(), humanInitiator(), f.sibling)
		resumed <- err
	}()
	within(t, humanWaiting, "the human's release never began")
	park()
	within(t, advanced, "the backoff's release never finished")
	select {
	case err := <-resumed:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("the human's resume never returned")
	}

	assert.Empty(t, f.c.CredentialHolds())
	assert.Equal(t, sortedCopy([]string{f.sibling, f.worker}), resumedHarps(t, f.c),
		"each held run is resumed once")
}

// TestRateHold_AResumeAtTheIdleInstantFindsTheLimitFolded forces the human's
// resume into the instant the run is about to read idle — held there at the
// turn-idle seam. The failure must already be in the hold, so the resume
// releases it and nothing reopens one after: a failure folded only after the
// run read idle would find the hold gone and open a stray one.
func TestRateHold_AResumeAtTheIdleInstantFindsTheLimitFolded(t *testing.T) {
	var target atomic.Pointer[string]
	atIdle := make(chan struct{})
	letGo := make(chan struct{})
	var once, release sync.Once
	t.Cleanup(func() { release.Do(func() { close(letGo) }) }) // a failed test must not leave the run stuck at the seam
	f, _ := newRateFixture(t, func(c *Coordinator) {
		c.turnIdleHook = func(harp string) {
			if p := target.Load(); p == nil || *p != harp {
				return
			}
			once.Do(func() { close(atIdle) })
			<-letGo
		}
	})
	target.Store(&f.sibling)

	f.send(t, f.sibling, limitHit+" do the work")
	within(t, atIdle, "the sibling's limited turn never reached its boundary")

	holds := f.c.CredentialHolds()
	require.Len(t, holds, 1, "at the idle instant the limit is already folded into a hold")
	assert.Contains(t, holds[0].Harps, f.sibling)

	_, err := f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)
	assert.Empty(t, f.c.CredentialHolds(), "the human's resume releases the hold")

	release.Do(func() { close(letGo) })
	require.Eventually(t, func() bool { return f.state(f.sibling) == StateIdle }, conformanceWait, 10*time.Millisecond)
	assert.Empty(t, f.c.CredentialHolds(), "nothing reopens a hold once the run reads idle")
}

// TestRateHold_ADrainingRunsLimitOpensNoHold pins the drain path: a run
// marked to end at its boundary whose last turn meets the limit ends there —
// it opens no hold, parks no sibling, and journals no hold event.
func TestRateHold_ADrainingRunsLimitOpensNoHold(t *testing.T) {
	f, clk := newRateFixture(t)
	runID := f.runOf(t, f.sibling)
	p := finalPolicy()
	f.c.mu.Lock()
	f.c.attach[runID].exitRequested = &p
	f.c.mu.Unlock()

	f.send(t, f.sibling, limitHit+" last work")
	require.Eventually(t, func() bool { return f.c.runEnded(runID) }, conformanceWait, 10*time.Millisecond)

	assert.Empty(t, f.c.CredentialHolds(), "a run ending at its boundary opens no hold")
	assert.Empty(t, journaled[holdOpened](t, f.c, factHoldOpened))
	assert.Zero(t, f.findingsWith("rate limit"), "%v", f.findings.All())
	assert.Zero(t, clk.Pending())
	f.send(t, f.worker, "work after the drain")
	awaitChatText(t, f.sp, 0, "work after the drain")
}

// The policy: the engine's reset time, clamped to [floor, cap] from now; the
// default when none was named.
func TestBackoffDeadline(t *testing.T) {
	now := fakeclock.Epoch
	for name, tc := range map[string]struct{ resets, want time.Time }{
		"none named":      {time.Time{}, now.Add(rateLimitDefault)},
		"in range":        {now.Add(10 * time.Minute), now.Add(10 * time.Minute)},
		"already past":    {now.Add(-time.Hour), now.Add(rateLimitFloor)},
		"too soon":        {now.Add(time.Second), now.Add(rateLimitFloor)},
		"at the floor":    {now.Add(rateLimitFloor), now.Add(rateLimitFloor)},
		"beyond the cap":  {now.Add(7 * 24 * time.Hour), now.Add(rateLimitCap)},
		"exactly the cap": {now.Add(rateLimitCap), now.Add(rateLimitCap)},
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, tc.want.Equal(backoffDeadline(now, tc.resets)), "got %v", backoffDeadline(now, tc.resets))
		})
	}
}

// The turn-idle value names a held failure; anything else is no failure.
func TestTurnFailureOf(t *testing.T) {
	assert.Nil(t, turnFailureOf(map[string]any{"stop_reason": "end_turn"}))
	assert.Nil(t, turnFailureOf(map[string]any{"stop_reason": "blocked"}))
	assert.Nil(t, turnFailureOf(map[string]any{"stop_reason": "not_a_held_kind"}), "a kind no hold releases is no held failure")
	assert.Equal(t, &agent.TurnFailure{Kind: agent.FailureRateLimited},
		turnFailureOf(map[string]any{"stop_reason": "rate_limited", TurnIdleResetsAt: "not a time"}))
	assert.Equal(t, &agent.TurnFailure{Kind: agent.FailureOverloaded}, turnFailureOf(map[string]any{"stop_reason": "overloaded"}))
	got := turnFailureOf(map[string]any{"stop_reason": "rate_limited", TurnIdleResetsAt: "2026-10-01T17:30:00Z"})
	require.NotNil(t, got)
	assert.True(t, time.Date(2026, 10, 1, 17, 30, 0, 0, time.UTC).Equal(got.ResetsAt))
}

// A run that carries no credential has nothing to share: its hold is its own.
func TestHoldKey_ARunWithNoCredentialHoldsAlone(t *testing.T) {
	assert.NotEqual(t, holdKey(agent.FailureRateLimited, engine.CredentialSource{}, "run-a"), holdKey(agent.FailureRateLimited, engine.CredentialSource{}, "run-b"))
	src := engine.Credentials{Env: map[string]string{"X": "v"}}.Source("e")
	assert.Equal(t, holdKey(agent.FailureRateLimited, src, "run-a"), holdKey(agent.FailureRateLimited, src, "run-b"))
}

// holdOf is harp's roster hold as the wire roster (listRunsSnapshot) shows it.
func (f *holdFixture) holdOf(t *testing.T, harp string) *RunHold {
	t.Helper()
	for _, r := range f.c.listRunsSnapshot(false, "", "").Runs {
		if r.Agent.AgentID == harp {
			return r.Hold
		}
	}
	t.Fatalf("%s is not in the roster", harp)
	return nil
}

// TestRateHold_TheRosterShowsTheHold: every run a hold parks carries it on its
// roster entry — the kind, the credential's source (names, never a value) and
// when it releases itself — a run on another credential carries none, and
// the release clears them.
func TestRateHold_TheRosterShowsTheHold(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	hold := f.awaitHold(t, f.worker, f.sibling)

	want := &RunHold{Kind: string(agent.FailureRateLimited), Source: hold.Source.Key, Until: limitResets}
	for _, harp := range []string{f.worker, f.sibling} {
		got := f.holdOf(t, harp)
		require.NotNil(t, got, "%s is held", harp)
		assert.Equal(t, want.Kind, got.Kind)
		assert.Equal(t, want.Source, got.Source)
		assert.True(t, want.Until.Equal(got.Until), "got %v", got.Until)
		assert.NotContains(t, got.Source, "=v", "the source names the carrier, never its value")
	}
	assert.Nil(t, f.holdOf(t, f.stranger), "a run on another credential is not held")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Nil(t, f.holdOf(t, f.worker), "the release clears the roster's hold")
	assert.Nil(t, f.holdOf(t, f.sibling))
}

// TestRateHold_TheInProcessRosterShowsTheHold: the root's in-process roster
// (Coordinator.Roster, what the overlay renders) carries the same hold as the
// wire roster, and a held run's state stays idle — held is not a phase.
func TestRateHold_TheInProcessRosterShowsTheHold(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	// The hold is in force before the worker's turn boundary marks it idle.
	require.Eventually(t, func() bool { return f.state(f.worker) == StateIdle }, conformanceWait, 10*time.Millisecond)

	for _, harp := range []string{f.worker, f.sibling} {
		e := f.entry(harp)
		require.NotNil(t, e.Hold, "%s is held", harp)
		assert.Equal(t, f.holdOf(t, harp), e.Hold, "one hold, both rosters")
		assert.Equal(t, StateIdle, e.State, "a held run stays idle")
	}
	assert.Nil(t, f.entry(f.stranger).Hold, "a run on another credential is not held")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Nil(t, f.entry(f.worker).Hold, "the release clears the roster's hold")
	assert.Nil(t, f.entry(f.sibling).Hold)
}

// TestRateHold_LivenessNeverJudgesAHeldRunStalled: a held run is waiting on
// its limit, not stuck — it takes the waiting-for-approval verdict, which
// outranks every stall rule, until the hold releases it.
func TestRateHold_LivenessNeverJudgesAHeldRunStalled(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)

	awaiting := func() map[string]bool {
		out := map[string]bool{}
		for _, tg := range f.c.livenessTargets() {
			out[tg.Harp] = tg.AwaitingApproval
		}
		return out
	}
	assert.Equal(t, map[string]bool{f.worker: true, f.sibling: true, f.stranger: false}, awaiting())
	verdict := reportFor(f.c.livenessSnapshot(context.Background()), f.sibling)
	require.NotNil(t, verdict)
	assert.Equal(t, liveness.StateAwaitingApproval, verdict.State, "a held run must never be judged stalled: %s", verdict.Reason)

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Equal(t, map[string]bool{f.worker: false, f.sibling: false, f.stranger: false}, awaiting(),
		"the release takes the exemption with it")
}

// TestRateHold_TheIdleReaperSparesHeldAndPausedRuns: a hold outlasting
// delegation.idle_timeout must not end the runs it parked — that would defeat
// the shared backoff — and neither may a human's pause. Time held or paused is
// not idle time: once released, a run is reapable again only after a full idle
// timeout. The clock is advanced and the sweep invoked, never awaited.
func TestRateHold_TheIdleReaperSparesHeldAndPausedRuns(t *testing.T) {
	const idle = 5 * time.Minute
	f, clk := newRateFixture(t, func(c *Coordinator) { c.idleTimeout = idle })
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	require.Eventually(t, func() bool { return f.state(f.worker) == StateIdle }, conformanceWait, 10*time.Millisecond)
	worker, sibling, stranger := f.runOf(t, f.worker), f.runOf(t, f.sibling), f.runOf(t, f.stranger)

	clk.Advance(idle + time.Minute) // past the idle timeout, short of the reset
	require.Len(t, f.c.CredentialHolds(), 1, "the hold still stands")
	f.c.reapIdleRuns()
	for _, h := range []string{f.worker, f.sibling, f.stranger} {
		assert.Equal(t, StateIdle, f.state(h), "a held or human-paused run is never idle-reaped")
	}

	clk.Advance(limitResets.Sub(clk.Now())) // the hold releases itself
	require.Empty(t, f.c.CredentialHolds())
	f.c.reapIdleRuns()
	assert.Equal(t, StateIdle, f.state(f.worker), "the time held does not count as idle")

	clk.Advance(idle)
	f.c.reapIdleRuns()
	assert.Equal(t, CauseIdleReaped, runCause(f.c, worker), "a released run is reapable again")
	assert.Equal(t, CauseIdleReaped, runCause(f.c, sibling), "a released run is reapable again")
	assert.Equal(t, StateIdle, f.state(f.stranger), "the human's pause still spares its run")

	_, err = f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	clk.Advance(idle)
	f.c.reapIdleRuns()
	assert.Equal(t, CauseIdleReaped, runCause(f.c, stranger), "a resumed run is reapable again")
}
