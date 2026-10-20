package cli

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/ctxloom/ctxloom/internal/testsupport/frontendfake"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// presenterSource is a coord.ApprovalSource whose events the test sends.
type presenterSource struct {
	mu      sync.Mutex
	pending []coord.PendingApproval
	events  chan coord.QueueEvent
}

func newPresenterSource(ps ...coord.PendingApproval) *presenterSource {
	return &presenterSource{pending: ps, events: make(chan coord.QueueEvent)}
}

func (s *presenterSource) Pending() []coord.PendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]coord.PendingApproval(nil), s.pending...)
	sort.Slice(out, func(i, j int) bool { return out[i].Deadline.Before(out[j].Deadline) })
	return out
}

func (s *presenterSource) Subscribe(context.Context) <-chan coord.QueueEvent { return s.events }
func (s *presenterSource) Answer(coord.ApprovalID, coord.ApprovalDecision) error {
	return nil
}
func (s *presenterSource) Grants(string) []coord.Grant { return nil }
func (s *presenterSource) Revoke(string, string) error { return nil }

// add parks p and tells the presenter, as the queue would. The send is
// unbuffered: when it returns, the presenter has the event.
func (s *presenterSource) add(p coord.PendingApproval) {
	s.mu.Lock()
	s.pending = append(s.pending, p)
	n := len(s.pending)
	s.mu.Unlock()
	s.events <- coord.QueueEvent{Kind: coord.QueueAdded, ID: p.ID, Pending: n}
}

func (s *presenterSource) resolve(id coord.ApprovalID, d agent.Decider) {
	s.mu.Lock()
	for i, p := range s.pending {
		if p.ID == id {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			break
		}
	}
	n := len(s.pending)
	s.mu.Unlock()
	s.events <- coord.QueueEvent{Kind: coord.QueueResolved, ID: id, Decider: d, Pending: n}
}

// settle returns once the presenter has finished every event sent before it:
// it sends a resolution of nothing (no note, the same count), and the send
// is unbuffered, so it completes only when the presenter's single loop is
// back at its select.
func (s *presenterSource) settle() {
	s.events <- coord.QueueEvent{Kind: coord.QueueResolved, Decider: agent.DeciderPolicy, Pending: len(s.Pending())}
}

// barCall is one SetApprovals.
type barCall struct {
	n       int
	oldest  time.Time
	arrived bool
}

// summonCall is one Summon; it stays in flight until its context ends.
type summonCall struct {
	notice string
	ctx    context.Context
}

// presenterUIFake records the presenter's calls on the terminal layer.
type presenterUIFake struct {
	mu      sync.Mutex
	bars    []barCall
	notes   []string
	summons chan summonCall
}

func newPresenterUIFake() *presenterUIFake {
	return &presenterUIFake{summons: make(chan summonCall, 16)}
}

func (f *presenterUIFake) ui() presenterUI {
	return presenterUI{
		summon: func(ctx context.Context, notice string) error {
			f.summons <- summonCall{notice: notice, ctx: ctx}
			<-ctx.Done()
			return ctx.Err()
		},
		noteBar: func(text string, d time.Duration) {
			f.mu.Lock()
			f.notes = append(f.notes, text)
			f.mu.Unlock()
		},
		setApprovals: func(n int, oldest time.Time, arrived bool) {
			f.mu.Lock()
			f.bars = append(f.bars, barCall{n, oldest, arrived})
			f.mu.Unlock()
		},
	}
}

func (f *presenterUIFake) lastBar() barCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bars) == 0 {
		return barCall{n: -1}
	}
	return f.bars[len(f.bars)-1]
}

func (f *presenterUIFake) noteList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

func (f *presenterUIFake) barCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bars)
}

func (f *presenterUIFake) nextSummon(t *testing.T) summonCall {
	t.Helper()
	select {
	case s := <-f.summons:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no summon")
		return summonCall{}
	}
}

func (f *presenterUIFake) noSummon(t *testing.T) {
	t.Helper()
	select {
	case s := <-f.summons:
		t.Fatalf("unexpected summon: %+v", s.notice)
	default:
	}
}

func pending(id, harp string, since, deadline time.Time) coord.PendingApproval {
	return coord.PendingApproval{ID: coord.ApprovalID(id), From: coord.Identity{Harp: harp}, Since: since, Deadline: deadline}
}

// presenterRun runs presentApprovals until the test ends.
type presenterRun struct {
	src *presenterSource
	ui  *presenterUIFake
	clk *fakeclock.Clock
	// finished closes when presentApprovals returns, with err its result.
	finished chan struct{}
	err      error
	stop     context.CancelFunc
}

func startPresenter(t *testing.T, src *presenterSource) *presenterRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &presenterRun{src: src, ui: newPresenterUIFake(), clk: fakeclock.New(), finished: make(chan struct{}), stop: cancel}
	go func() {
		defer close(r.finished)
		r.err = presentApprovals(ctx, src, r.ui.ui(), r.clk)
	}()
	t.Cleanup(func() {
		cancel()
		<-r.finished
	})
	return r
}

// waitTick waits until the presenter has armed its tick.
func (r *presenterRun) waitTick(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return r.clk.Pending() > 0 }, 5*time.Second, time.Millisecond)
}

var t0 = fakeclock.Epoch

// TestPresenter_StartsWithTheBarAndTheModalForWhatIsAlreadyPending: a
// presenter that starts over parked requests shows them at once — the panel's
// count and oldest arrival, and a summon naming the soonest-deadline asker.
func TestPresenter_StartsWithTheBarAndTheModalForWhatIsAlreadyPending(t *testing.T) {
	src := newPresenterSource(
		pending("b", "calm-heron", t0.Add(-10*time.Second), t0.Add(9*time.Minute)),
		pending("a", "wiry-otter", t0.Add(-65*time.Second), t0.Add(5*time.Minute)),
	)
	r := startPresenter(t, src)
	s := r.ui.nextSummon(t)
	assert.Equal(t, "approval from wiry-otter", s.notice)
	assert.Equal(t, barCall{n: 2, oldest: t0.Add(-65 * time.Second)}, r.ui.lastBar(), "no bell for what was already waiting")
}

// TestPresenter_AnArrivalRingsAndSummonsForTheNewAsker: each arrival rings
// (the split view rate-limits it) and replaces an in-flight summon with one
// naming the new asker — so a human in the tree is told who is asking now.
func TestPresenter_AnArrivalRingsAndSummonsForTheNewAsker(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	first := r.ui.nextSummon(t)
	src.add(pending("b", "calm-heron", t0.Add(time.Second), t0.Add(10*time.Minute)))
	second := r.ui.nextSummon(t)
	assert.Equal(t, "approval from calm-heron", second.notice)
	assert.Error(t, first.ctx.Err(), "the earlier summon was withdrawn")
	assert.Equal(t, barCall{n: 2, oldest: t0, arrived: true}, r.ui.lastBar())
}

// TestPresenter_AnArrivalAlreadyPresentedAtStartIsNotSummonedAgain: a
// request parked before the presenter starts is shown by its start, and its
// arrival event, delivered after, is the same request — not a second
// arrival. Summoning it again would bring back a modal the human closed.
func TestPresenter_AnArrivalAlreadyPresentedAtStartIsNotSummonedAgain(t *testing.T) {
	req := pending("a", "wiry-otter", t0, t0.Add(9*time.Minute))
	src := newPresenterSource(req)
	r := startPresenter(t, src)
	first := r.ui.nextSummon(t)
	bars := r.ui.barCount()

	src.events <- coord.QueueEvent{Kind: coord.QueueAdded, ID: req.ID, Pending: 1}
	src.settle()
	require.NoError(t, first.ctx.Err(), "the start's summon is not withdrawn for a request it already shows")
	assert.False(t, r.ui.lastBar().arrived, "no bell: nothing arrived")

	// Resolving the request withdraws every summon in flight and waits for
	// it, so any summon the event started has been made by now.
	src.resolve(req.ID, agent.DeciderHuman)
	src.settle()
	r.ui.noSummon(t)
	assert.Greater(t, r.ui.barCount(), bars, "the panel still follows the queue")
}

// TestPresenter_AnArrivalIsSummonedOnce: one arrival event per request; a
// repeat of it is not a new arrival.
func TestPresenter_AnArrivalIsSummonedOnce(t *testing.T) {
	src := newPresenterSource()
	r := startPresenter(t, src)
	src.settle()
	req := pending("a", "wiry-otter", t0, t0.Add(9*time.Minute))
	src.add(req)
	first := r.ui.nextSummon(t)
	src.events <- coord.QueueEvent{Kind: coord.QueueAdded, ID: req.ID, Pending: 1}
	src.settle()
	require.NoError(t, first.ctx.Err(), "a repeated arrival does not replace the summon")
	src.resolve(req.ID, agent.DeciderHuman)
	src.settle()
	r.ui.noSummon(t)
}

// TestPresenter_AResolvedRequestIsForgotten: what the presenter remembers
// as shown is the pending requests; one parked again under the same id after
// it resolved is an arrival again. (The queue mints a fresh id per park
// today; this keeps the presenter from depending on that.)
func TestPresenter_AResolvedRequestIsForgotten(t *testing.T) {
	src := newPresenterSource()
	r := startPresenter(t, src)
	src.settle()
	req := pending("a", "wiry-otter", t0, t0.Add(9*time.Minute))
	src.add(req)
	r.ui.nextSummon(t)
	src.resolve(req.ID, agent.DeciderHuman)
	src.add(req)
	s := r.ui.nextSummon(t)
	assert.Equal(t, "approval from wiry-otter", s.notice)
}

// TestPresenter_NothingPendingWithdrawsTheSummon: when the last request
// resolves before the approvals view could appear, it never appears, and the panel
// clears.
func TestPresenter_NothingPendingWithdrawsTheSummon(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	s := r.ui.nextSummon(t)
	src.resolve("a", agent.DeciderTimeout)
	require.Eventually(t, func() bool { return s.ctx.Err() != nil }, 5*time.Second, time.Millisecond)
	assert.Equal(t, barCall{n: 0}, r.ui.lastBar())
	r.ui.noSummon(t)
}

// TestPresenter_ATimedOutRequestLeavesABarNote: nobody decided it, so the
// bar says so once the approvals view is gone; a withdrawn or decided request does not.
func TestPresenter_ATimedOutRequestLeavesABarNote(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)), pending("b", "calm-heron", t0, t0.Add(9*time.Minute)), pending("c", "kind-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	r.ui.nextSummon(t)
	src.resolve("a", agent.DeciderHuman)
	src.resolve("b", agent.DeciderCancelled)
	src.resolve("c", agent.DeciderTimeout)
	require.Eventually(t, func() bool { return len(r.ui.noteList()) > 0 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"approval resolved (timed out)"}, r.ui.noteList())
}

// TestPresenter_TheBarsAgeKeepsCounting: while anything is pending the panel
// is refreshed every second, with no bell.
func TestPresenter_TheBarsAgeKeepsCounting(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	r.ui.nextSummon(t)
	r.waitTick(t)
	before := r.ui.barCount()
	r.clk.Advance(time.Second)
	require.Eventually(t, func() bool { return r.ui.barCount() > before }, 5*time.Second, time.Millisecond)
	assert.Equal(t, barCall{n: 1, oldest: t0}, r.ui.lastBar())
}

// TestPresenter_TheTwoMinuteWarningRingsAndSummonsOnce: a request reaching
// two minutes left re-summons and rings once — never again for it.
func TestPresenter_TheTwoMinuteWarningRingsAndSummonsOnce(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(2*time.Minute+2*time.Second)))
	r := startPresenter(t, src)
	r.ui.nextSummon(t)
	for i := 0; i < 2; i++ {
		r.waitTick(t)
		r.clk.Advance(time.Second)
	}
	warn := r.ui.nextSummon(t)
	assert.Equal(t, "approval from wiry-otter", warn.notice)
	require.Eventually(t, func() bool { return r.ui.lastBar().arrived }, 5*time.Second, time.Millisecond, "the warning rings")
	for i := 0; i < 5; i++ {
		r.waitTick(t)
		r.clk.Advance(time.Second)
	}
	r.ui.noSummon(t)
}

// TestPresenter_ReturnsWhenItsContextEnds and withdraws its summon.
func TestPresenter_ReturnsWhenItsContextEnds(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	s := r.ui.nextSummon(t)
	r.stop()
	select {
	case <-r.finished:
		require.NoError(t, r.err)
	case <-time.After(5 * time.Second):
		t.Fatal("Present did not return")
	}
	assert.Error(t, s.ctx.Err())
}

// viewSource is the presenter's queue as the split view reads it: its own
// subscription, which no test event reaches, so every event the test sends
// is the presenter's.
type viewSource struct{ *presenterSource }

func (viewSource) Subscribe(context.Context) <-chan coord.QueueEvent {
	return make(chan coord.QueueEvent)
}

// paneTerm is the engine's terminal under the split view: it takes what the
// view writes and never yields output.
type paneTerm struct{}

func (paneTerm) Write(p []byte) (int, error) { return len(p), nil }
func (paneTerm) Read([]byte) (int, error)    { select {} }
func (paneTerm) Resize(uint16, uint16) error { return nil }

// humanTTY is the human's terminal under the split view: the screen the
// program's output draws, re-checked on every write.
type humanTTY struct {
	mu     sync.Mutex
	screen *vtemu.Screen
	wake   chan struct{}
}

func newHumanTTY(cols, rows int) *humanTTY {
	return &humanTTY{screen: vtemu.New(rows, cols), wake: make(chan struct{})}
}

// Write draws p; bubbletea writes to a non-tty as to a cooked terminal,
// relying on ONLCR, so the mapping is done here.
func (h *humanTTY) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.screen.Feed([]byte(strings.ReplaceAll(string(p), "\n", "\r\n")))
	close(h.wake)
	h.wake = make(chan struct{})
	return len(p), nil
}

// waitScreen blocks until the screen satisfies cond.
func (h *humanTTY) waitScreen(t *testing.T, what string, cond func(screen string) bool) {
	t.Helper()
	expired := testsupport.Expiry(t)
	for {
		h.mu.Lock()
		screen, wake := h.screen.String(), h.wake
		h.mu.Unlock()
		if cond(screen) {
			return
		}
		select {
		case <-wake:
		case <-expired:
			t.Fatalf("the screen never showed %s:\n%s", what, screen)
		}
	}
}

// TestModalPresenter_SummonsTheApprovalsViewOnTheRealSplitView joins the
// presenter to a real split view: a request already parked puts its count
// on the panel and the approvals view in the pane, inert while it arms; the
// human closes it, and the request's timing out leaves the note on the
// panel.
func TestModalPresenter_SummonsTheApprovalsViewOnTheRealSplitView(t *testing.T) {
	clk := fakeclock.New()
	req := pending("a", "wiry-otter", clk.Now().Add(-5*time.Second), clk.Now().Add(10*time.Minute))
	src := newPresenterSource(req)
	prefix, err := tui.ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	stdinR, stdinW := io.Pipe()
	tty := newHumanTTY(100, 24)
	view, err := tui.StartSplit(context.Background(), tui.SplitOptions{
		Port: &frontendfake.Port{ApprovalSource: viewSource{src}}, Self: "root-otter", Term: paneTerm{},
		Prefix: prefix, Clock: clk, In: stdinR, Out: tty, WindowSize: [2]int{100, 24},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		view.Close()
		_ = stdinW.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- modalPresenter{ui: view, clock: clk}.Present(ctx, src) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	tty.waitScreen(t, "the summoned approvals view, arming, beside the panel's count", func(s string) bool {
		return strings.Contains(s, "! 1 approval") && strings.Contains(s, "wiry-otter") && strings.Contains(s, "arming —")
	})
	clk.Advance(time.Second)
	tty.waitScreen(t, "the approvals view armed", func(s string) bool { return !strings.Contains(s, "arming —") })
	_, err = stdinW.Write([]byte("\x1b"))
	require.NoError(t, err)
	tty.waitScreen(t, "the approvals view closed", func(s string) bool { return !strings.Contains(s, "wiry-otter") })

	src.resolve("a", agent.DeciderTimeout)
	src.settle()
	tty.waitScreen(t, "the timed-out note", func(s string) bool { return strings.Contains(s, "approval resolved (timed") })
}
