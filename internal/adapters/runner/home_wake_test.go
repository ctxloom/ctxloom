package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The runner owns everything about a wake that does not depend on the
// engine: fire only when mail is pending, arm the nonce BEFORE firing, keep
// one wake in flight, disarm a wake that never went out, and disarm one that
// went out and was never answered (wakeAnswerWait).
//
// Every interleaving below is forced: the sweep is called on the test's own
// goroutine, the wake records synchronously, and the alarm is captured and
// run by hand — nothing waits on a timer or polls shared state.

// recordingWake reports each nonce it is fired with, and whether that nonce
// was already armed on disk at the moment of firing.
type recordingWake struct {
	h     *Home
	err   error
	fired chan string
	armed atomic.Bool
}

func (w *recordingWake) Fire(_ context.Context, nonce string) error {
	out, _ := spool.OutstandingWake(w.h.cfg.Mapper, w.h.Harp())
	for _, n := range out {
		if n == nonce {
			w.armed.Store(true)
		}
	}
	w.fired <- nonce
	return w.err
}

// alarms captures every wakeAnswerWait alarm the Home arms, so a test runs
// the expiry exactly when it chooses.
type alarms struct {
	waits []time.Duration
	fns   []func()
}

func (a *alarms) arm(d time.Duration, f func()) {
	a.waits = append(a.waits, d)
	a.fns = append(a.fns, f)
}

// newOwnerHome is the session owner's Home: it hosts the interactive run, its
// spool is its turn-start hook's, and w is its registered wake.
func newOwnerHome(t *testing.T) (*Home, *recordingWake, *alarms, *lockedFindings) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newNoticeHome(t)
	found := &lockedFindings{}
	h.rep = report.To(found)
	h.identity = ownerIdentity()
	h.cfg.Mapper = spool.NewHomeMapper()
	al := &alarms{}
	h.wakeAlarm = al.arm
	h.markOwner()
	w := &recordingWake{h: h, fired: make(chan string, 4)}
	h.SetWake(w)
	return h, w, al, found
}

func seedOwnerIn(t *testing.T, h *Home) {
	t.Helper()
	wr, err := spool.NewWriter(h.cfg.Mapper, h.Harp(), spool.DirIn, "coord")
	require.NoError(t, err)
	_, err = wr.Write(&spool.Message{Kind: "message", FromHarp: "child", To: h.Harp(), Body: "hi\n"})
	require.NoError(t, err)
}

func outstandingOf(t *testing.T, h *Home) []string {
	t.Helper()
	out, err := spool.OutstandingWake(h.cfg.Mapper, h.Harp())
	require.NoError(t, err)
	return out
}

// fired takes the one nonce the wake was fired with, or fails.
func fired(t *testing.T, w *recordingWake) string {
	t.Helper()
	select {
	case n := <-w.fired:
		return n
	default:
		t.Fatal("the wake was not fired")
		return ""
	}
}

func warningsOf(found *lockedFindings) []string {
	var out []string
	for _, f := range found.all() {
		out = append(out, f.Text)
	}
	return out
}

func TestHomeWake_MailForTheOwnerFiresAnArmedWake(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	seedOwnerIn(t, h)

	h.sweepSpoolIn()

	nonce := fired(t, w)
	assert.True(t, w.armed.Load(), "the nonce is on disk before the wake fires")
	assert.Equal(t, []string{nonce}, outstandingOf(t, h), "…and stays armed until the hook redeems it")
}

// TestHomeWake_OwnerMailIsNotBuffered: the owner's spool IS its store — its
// turn-start hook reads it — so the runner holds no copy of its mail.
// MUTATION — route the owner's sweep through deliverNotice's buffer — turns
// this red.
func TestHomeWake_OwnerMailIsNotBuffered(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	seedOwnerIn(t, h)

	h.sweepSpoolIn()
	fired(t, w)

	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Empty(t, h.buffer, "the owner's mail stays in its spool for the hook; a runner copy is one nobody reads")
	assert.Empty(t, h.spoolRefs, "nothing of the owner's in/ is the runner's to acknowledge")
}

func TestHomeWake_OneWakeInFlightIsEnough(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	fired(t, w)
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	assert.Empty(t, w.fired, "the outstanding wake's hook drains everything; a second wake would be a wasted turn")
	assert.Len(t, outstandingOf(t, h), 1)
}

func TestHomeWake_AWakeThatFailsIsDisarmed(t *testing.T) {
	h, w, al, _ := newOwnerHome(t)
	w.err = errors.New("no relay is subscribed")
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	fired(t, w)
	assert.Empty(t, outstandingOf(t, h), "a wake that never went out must not block the next one")
	assert.Empty(t, al.fns, "a wake that never went out has nothing to answer, so no alarm")
}

func TestHomeWake_NothingPendingFiresNothing(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	h.sweepSpoolIn()
	assert.Empty(t, w.fired)
	assert.Empty(t, outstandingOf(t, h))
}

// TestHomeWake_AnUnansweredWakeIsDisarmedSoTheNextMailCanWake is F1: a wake
// that went out and was held (an engine's approval prompt), or reached no
// one, must not block every later wake. The alarm warns ONCE, naming the
// likely causes, and disarms the nonce.
// MUTATION — skip the disarm in the alarm — leaves the nonce on disk, and
// the second mail's wake is refused: red.
func TestHomeWake_AnUnansweredWakeIsDisarmedSoTheNextMailCanWake(t *testing.T) {
	h, w, al, found := newOwnerHome(t)
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	first := fired(t, w)
	require.Len(t, al.fns, 1, "a wake that went out arms its answer alarm")
	assert.Equal(t, wakeAnswerWait, al.waits[0])

	al.fns[0]() // the wait expires with the nonce still on disk

	assert.Empty(t, outstandingOf(t, h), "the unanswered nonce is disarmed")
	warns := warningsOf(found)
	require.Len(t, warns, 1, "warned once")
	assert.Contains(t, warns[0], first)
	assert.Contains(t, warns[0], "held")
	assert.Contains(t, warns[0], "subscribed")

	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	assert.NotEqual(t, first, fired(t, w), "the next mail wakes again")
}

// TestHomeWake_AnAnsweredWakeRaisesNoAlarm: the hook redeemed the nonce
// before the wait ran out — the alarm finds nothing and says nothing.
// MUTATION — warn without checking the nonce was still armed — turns this red.
func TestHomeWake_AnAnsweredWakeRaisesNoAlarm(t *testing.T) {
	h, w, al, found := newOwnerHome(t)
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	nonce := fired(t, w)
	redeemed, err := spool.ConsumeWake(h.cfg.Mapper, h.Harp(), nonce)
	require.NoError(t, err)
	require.True(t, redeemed)

	al.fns[0]()

	assert.Empty(t, warningsOf(found), "an answered wake is not reported")
}

// TestHomeWake_ALaterRegistrationReplacesTheEarlier: claude can respawn its
// relay, and the new relay registers again. The later registration is the
// live one; the earlier one's release must not clear it.
// MUTATION — release clears unconditionally — turns this red.
func TestHomeWake_ALaterRegistrationReplacesTheEarlier(t *testing.T) {
	h, first, _, _ := newOwnerHome(t) // first registered by the helper
	second := &recordingWake{h: h, fired: make(chan string, 1)}
	releaseSecond := h.SetWake(second)

	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	fired(t, second)
	assert.Empty(t, first.fired, "the replaced registration is never fired")

	stale := h.SetWake(first)
	h.SetWake(second)
	stale() // the first's release, after it was replaced again: a no-op
	assert.Same(t, second, h.currentWake(), "an earlier registration's release leaves the later one bound")

	releaseSecond()
	assert.Same(t, second, h.currentWake(), "a release from a superseded registration of the same wake is a no-op too")
}

// TestHomeWake_ReleasingTheCurrentRegistrationUnbinds: the relay went away
// and nothing replaced it; mail then waits for the next prompt.
func TestHomeWake_ReleasingTheCurrentRegistrationUnbinds(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	release := h.SetWake(w)
	release()
	assert.Nil(t, h.currentWake())
	seedOwnerIn(t, h)
	h.sweepSpoolIn()
	assert.Empty(t, w.fired)
}

// TestHomeWake_NoWakeWhileTheOwnerIsAwayAndItsReturnWakes: no new turn
// starts while the owner's link is down; its return wakes for what waited.
func TestHomeWake_NoWakeWhileTheOwnerIsAwayAndItsReturnWakes(t *testing.T) {
	h, w, _, _ := newOwnerHome(t)
	h.ownerUp, h.present = false, make(chan struct{})
	seedOwnerIn(t, h)

	h.sweepSpoolIn()
	require.Empty(t, w.fired, "no wake — no new turn — while the owner is away")

	h.ownerUp = true
	h.sweepSpoolIn()
	fired(t, w)
}

// TestHomeWake_AHostedRunThatIsNotTheOwnerStillBuffers: a delegated child's
// mail arriving before its engine registers a turn sink waits in the buffer
// for that sink — the owner rule does not reach it.
func TestHomeWake_AHostedRunThatIsNotTheOwnerStillBuffers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newNoticeHome(t)
	h.identity = ownerIdentity()
	h.cfg.Mapper = spool.NewHomeMapper()
	seedOwnerIn(t, h)

	h.sweepSpoolIn()

	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Len(t, h.buffer, 1)
}
