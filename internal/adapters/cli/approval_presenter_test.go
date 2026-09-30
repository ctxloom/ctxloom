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

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
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

// barCall is one SetApprovals.
type barCall struct {
	n       int
	oldest  time.Time
	arrived bool
}

// summonCall is one Summon; it stays in flight until its context ends.
type summonCall struct {
	start  termui.OverlayStart
	notice termui.Notice
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
		summon: func(ctx context.Context, start termui.OverlayStart, n termui.Notice) error {
			f.summons <- summonCall{start: start, notice: n, ctx: ctx}
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
// presenter that starts over parked requests shows them at once — the bar's
// count and oldest arrival, and a summon naming the soonest-deadline asker.
func TestPresenter_StartsWithTheBarAndTheModalForWhatIsAlreadyPending(t *testing.T) {
	src := newPresenterSource(
		pending("b", "calm-heron", t0.Add(-10*time.Second), t0.Add(9*time.Minute)),
		pending("a", "wiry-otter", t0.Add(-65*time.Second), t0.Add(5*time.Minute)),
	)
	r := startPresenter(t, src)
	s := r.ui.nextSummon(t)
	assert.Equal(t, termui.OverlayStart{View: "approvals"}, s.start)
	assert.Equal(t, termui.Notice{Text: "approval from wiry-otter"}, s.notice)
	assert.Equal(t, barCall{n: 2, oldest: t0.Add(-65 * time.Second)}, r.ui.lastBar(), "no bell for what was already waiting")
}

// TestPresenter_AnArrivalRingsAndSummonsForTheNewAsker: each arrival rings
// (the bar rate-limits it) and replaces an in-flight summon with one naming
// the new asker — so a human in the overlay is told who is asking now.
func TestPresenter_AnArrivalRingsAndSummonsForTheNewAsker(t *testing.T) {
	src := newPresenterSource(pending("a", "wiry-otter", t0, t0.Add(9*time.Minute)))
	r := startPresenter(t, src)
	first := r.ui.nextSummon(t)
	src.add(pending("b", "calm-heron", t0.Add(time.Second), t0.Add(10*time.Minute)))
	second := r.ui.nextSummon(t)
	assert.Equal(t, termui.Notice{Text: "approval from calm-heron"}, second.notice)
	assert.Error(t, first.ctx.Err(), "the earlier summon was withdrawn")
	assert.Equal(t, barCall{n: 2, oldest: t0, arrived: true}, r.ui.lastBar())
}

// TestPresenter_NothingPendingWithdrawsTheSummon: when the last request
// resolves before the modal could appear, it never appears, and the bar
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
// bar says so once the modal is gone; a withdrawn or decided request does not.
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

// TestPresenter_TheBarsAgeKeepsCounting: while anything is pending the bar
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
	assert.Equal(t, termui.Notice{Text: "approval from wiry-otter"}, warn.notice)
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

// fakeModal is the overlay the controller builds; it stays up until aborted.
type fakeModal struct {
	quit chan struct{}
	once sync.Once
}

func (m *fakeModal) Run(in io.Reader, _ io.Writer, _ termui.OverlayGeometry) error {
	go func() { _, _ = io.Copy(io.Discard, in) }()
	<-m.quit
	return nil
}
func (m *fakeModal) Abort()                        { m.once.Do(func() { close(m.quit) }) }
func (m *fakeModal) Resize(termui.OverlayGeometry) {}
func (m *fakeModal) Armed(int)                     {}
func (m *fakeModal) Notify(termui.Notice)          {}

// lockedTTY is the controller's terminal.
type lockedTTY struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedTTY) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedTTY) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestModalPresenter_SummonsTheModalOnTheRealController joins the presenter
// to a real terminal layer: an arrival puts "⚑ 1 · oldest" on the bar and
// the approvals modal on the screen, summoned; its timing out leaves the
// note on the bar.
func TestModalPresenter_SummonsTheModalOnTheRealController(t *testing.T) {
	clk := fakeclock.New()
	starts := make(chan termui.OverlayStart, 4)
	modal := &fakeModal{quit: make(chan struct{})}
	stdinR, stdinW := io.Pipe()
	tty := &lockedTTY{}
	sizes := make(chan *agent.WindowSize, 1)
	ui := termui.New(termui.Options{
		Stdin: stdinR, TTY: tty, Resize: sizes, Prefix: 0x1d, Surround: true, Clock: clk,
		NewOverlay: func(s termui.OverlayStart) termui.Overlay { starts <- s; return modal },
	})
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		_, _ = io.Copy(io.Discard, ui.Stdin())
	}()
	go func() {
		for range ui.Resize() {
		}
	}()
	t.Cleanup(func() {
		ui.Close()
		_ = stdinW.Close()
		close(sizes)
		<-pumpDone
	})
	sizes <- &agent.WindowSize{Rows: 24, Cols: 100}
	require.Eventually(t, func() bool { return strings.Contains(tty.String(), "\x1b[1;23r") }, 5*time.Second, time.Millisecond, "surround established")

	src := newPresenterSource()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- modalPresenter{ui: ui, clock: clk}.Present(ctx, src) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	src.add(pending("a", "wiry-otter", clk.Now().Add(-5*time.Second), clk.Now().Add(10*time.Minute)))
	select {
	case s := <-starts:
		assert.Equal(t, termui.OverlayStart{Summoned: true, View: "approvals"}, s)
	case <-time.After(5 * time.Second):
		t.Fatal("the modal was never summoned")
	}
	require.Eventually(t, func() bool { return strings.Contains(tty.String(), "⚑ 1 · oldest 00:05") }, 5*time.Second, time.Millisecond)
	modal.Abort()
	src.resolve("a", agent.DeciderTimeout)
	require.Eventually(t, func() bool { return strings.Contains(tty.String(), "approval resolved (timed out)") }, 5*time.Second, time.Millisecond,
		"nobody decided it: the bar says so; tail %q", func() string { t := tty.String(); return t[max(len(t)-600, 0):] }())
}
