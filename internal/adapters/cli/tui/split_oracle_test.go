package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/termoracle"
)

// The oracle's tables, taken from the terminal layer the split view
// replaces, run here against the split view.

var daReply = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)

// settleEngine returns everything the engine has received once every reply
// the emulator queued before now has arrived: replies cross one pipe in
// order, so a device-attributes answer asked for now comes last.
func (h *splitHarness) settleEngine(t *testing.T) string {
	t.Helper()
	_, _ = h.pane.Write([]byte("\x1b[c"))
	expired := testsupport.Expiry(t)
	for {
		h.engine.mu.Lock()
		got, wake := h.engine.buf.String(), h.engine.wake
		h.engine.mu.Unlock()
		if loc := daReply.FindStringIndex(got); loc != nil {
			h.engine.take()
			return got[:loc[0]] + got[loc[1]:]
		}
		select {
		case <-wake:
		case <-expired:
			t.Fatalf("no device-attributes reply; got %q", got)
		}
	}
}

// TestSplitOracle_TokenCorpusWithThePaneFocused: with the engine's pane
// focused, every key reaches the engine as typed, and a paste whole (the
// engine asked for brackets). Where the old layer passed a token through and
// the split view does not, it is by design: a terminal reply answers the
// split view's own query — the engine's queries are the emulator's to answer
// — and every corpus mouse report lands on the panel's columns.
func TestSplitOracle_TokenCorpusWithThePaneFocused(t *testing.T) {
	for _, tk := range termoracle.Tokens {
		t.Run(tk.Name, func(t *testing.T) {
			h := newSplitHarness(t, newSplitPort()).sized(100, 12)
			h.engineWrites("\x1b[?2004h\x1b[?1000h\x1b[?1006h")
			h.settleEngine(t)
			if tk.Kind == termoracle.Paste {
				body := strings.TrimSuffix(strings.TrimPrefix(tk.Bytes, "\x1b[200~"), "\x1b[201~")
				h.send(tea.PasteMsg{Content: body})
			} else {
				h.typeBytes(tk.Bytes)
			}
			got := h.settleEngine(t)
			switch tk.Kind {
			case termoracle.Key, termoracle.Paste:
				assert.Equal(t, tk.Bytes, got)
			case termoracle.Report, termoracle.Mouse:
				assert.Empty(t, got)
			}
		})
	}
}

// TestSplitOracle_SummonContract runs the approvals focus-lock contract
// against the split view.
func TestSplitOracle_SummonContract(t *testing.T) {
	termoracle.RunSummonContract(t, func(t *testing.T) termoracle.Summoner {
		port := newSplitPort()
		port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour), bashReq("a2", "brave-heron", "pwd", 2*time.Hour))
		h := newSplitHarness(t, port).sized(100, 30)
		return &splitSummoner{t: t, h: h}
	})
}

// splitSummoner is the split view as the contract drives it: its summons go
// through the same messages SplitView.Summon sends, and a cancelled one is
// withdrawn at the next step, as SplitView's waiter would.
type splitSummoner struct {
	t *testing.T
	h *splitHarness
	// engine is everything the engine has received, settled.
	engine string
	ids    uint64
	ctx    context.Context
	id     uint64
	closed bool
}

func (s *splitSummoner) withdraw() {
	if s.ctx != nil && s.ctx.Err() != nil {
		s.h.send(summonCancelMsg{id: s.id})
		s.ctx = nil
	}
}

func (s *splitSummoner) Type(t *testing.T, keys string) {
	t.Helper()
	s.withdraw()
	s.h.typeBytes(keys)
}

func (s *splitSummoner) Engine() string {
	s.engine += s.h.settleEngine(s.t)
	return s.engine
}

func (s *splitSummoner) Summon(ctx context.Context) <-chan error {
	out := make(chan error, 1)
	if s.closed {
		out <- ErrUnavailable
		return out
	}
	s.ids++
	s.ctx, s.id = ctx, s.ids
	reply := make(chan error, 1)
	s.h.send(summonMsg{id: s.id, notice: termoracle.Notice, reply: reply})
	go func() {
		select {
		case err := <-reply:
			out <- err
		case <-ctx.Done():
			out <- ctx.Err()
		}
	}()
	return out
}

func (s *splitSummoner) Parked(t *testing.T) {
	t.Helper()
	require.NotNil(t, s.h.s.summon, "no summon is waiting")
}

func (s *splitSummoner) Advance(t *testing.T, d time.Duration) {
	t.Helper()
	s.withdraw()
	s.h.advance(d)
}

func (s *splitSummoner) Outcome(err error) termoracle.Outcome {
	switch {
	case err == nil:
		return termoracle.Shown
	case errors.Is(err, ErrEngaged):
		return termoracle.Engaged
	case errors.Is(err, context.Canceled):
		return termoracle.Cancelled
	}
	return termoracle.Unavailable
}

func (s *splitSummoner) Shown() bool { return s.h.s.apprOpen && s.h.s.apprSummoned }

func (s *splitSummoner) Armed() (bool, int) {
	return s.Shown() && !s.h.s.arming, s.h.s.armDiscarded
}

// Approval is the keys the approvals view acted on, read off its selection:
// the contract types only j once armed, and each j moves it down one.
func (s *splitSummoner) Approval() string {
	if !s.Shown() {
		return ""
	}
	return strings.Repeat("j", s.h.s.appr.selectedIndex())
}

func (s *splitSummoner) Engage(t *testing.T) {
	t.Helper()
	s.h.typeBytes("\x1d")
}

func (s *splitSummoner) Notices() []string {
	if strings.Contains(s.h.s.banner, termoracle.Notice) {
		return []string{termoracle.Notice}
	}
	return nil
}

func (s *splitSummoner) Close() { s.closed = true }

// TestSplit_KeysForThePaneNeverAnswerAnApproval pins the focus lock end to
// end: Enter after Enter typed for the engine while an approval waits all
// reach the engine; the approval shows only after QuietFor of quiet; an
// Enter in its inert window is swallowed; and armed, Enter on its neutral
// action hides it without deciding. Nothing reaches the queue.
func TestSplit_KeysForThePaneNeverAnswerAnApproval(t *testing.T) {
	port := newSplitPort()
	q := newFakeQueue(bashReq("a1", "brave-heron", "rm -rf build", time.Hour))
	port.queue = q
	h := newSplitHarness(t, port).sized(100, 30)
	reply := make(chan error, 1)
	h.typeBytes("\r")
	h.send(summonMsg{id: 1, notice: "approval from brave-heron", reply: reply})
	for range 5 {
		h.advance(time.Second)
		h.typeBytes("\r")
	}
	assert.Equal(t, strings.Repeat("\r", 6), h.settleEngine(t), "every Enter went to the engine")
	assert.Empty(t, reply)

	h.advance(termoracle.QuietFor)
	require.NoError(t, <-reply)
	require.True(t, h.s.apprOpen)
	assert.Contains(t, strings.Join(paneText(h), "\n"), "keys come here, not to the engine", "the approval holds the pane")
	h.typeBytes("\r")
	assert.Equal(t, 1, h.s.armDiscarded)
	h.advance(termoracle.ArmFor)
	require.False(t, h.s.arming)
	h.typeBytes("\r")
	assert.False(t, h.s.apprOpen, "Enter on the neutral action hides the view")
	assert.Equal(t, focusPane, h.s.focus, "and focus is the pane's again")
	assert.Empty(t, q.calls(), "nothing was decided")
	assert.Empty(t, h.settleEngine(t), "nor did anything typed at the approval reach the engine")
}

// A summon replaced by a newer one says so; the newer is the one shown.
func TestSplit_ANewerSummonReplacesAWaitingOne(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.typeBytes("x")
	first, second := make(chan error, 1), make(chan error, 1)
	h.send(summonMsg{id: 1, reply: first})
	h.send(summonMsg{id: 2, reply: second})
	assert.ErrorIs(t, <-first, errSummonReplaced)
	h.advance(termoracle.QuietFor)
	assert.NoError(t, <-second)
}

// The tree's a opens the approvals view at once, with no inert window — the
// human asked for it — and hiding it hands focus back to the tree.
func TestSplit_ApprovalsFromTheTree(t *testing.T) {
	port := newSplitPort()
	q := newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	port.queue = q
	h := newSplitHarness(t, port).sized(100, 30)
	h.typeBytes("\x1d")
	h.keys("a")
	require.True(t, h.s.apprOpen)
	assert.False(t, h.s.arming)
	h.keys("esc")
	assert.False(t, h.s.apprOpen)
	assert.Equal(t, focusList, h.s.focus)

	// The queue emptying closes it.
	h.keys("a")
	q.resolve("a1")
	h.send(queueEventMsg{ev: queueResolved("a1"), ok: true})
	h.advance(tombstoneFor)
	h.send(approvalsTickMsg{})
	assert.False(t, h.s.apprOpen)
	assert.True(t, h.panelHas("no approvals waiting"))

	port2 := newSplitPort()
	h2 := newSplitHarness(t, port2).sized(100, 30)
	h2.typeBytes("\x1d")
	h2.keys("a")
	assert.False(t, h2.s.apprOpen)
	assert.True(t, h2.panelHas("approvals unavailable"))
}

// With no approval queue a summon is refused as unavailable.
func TestSplit_SummonWithNoQueueIsUnavailable(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 30)
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, reply: reply})
	assert.ErrorIs(t, <-reply, ErrUnavailable)
}

func queueResolved(id string) coord.QueueEvent {
	return coord.QueueEvent{Kind: coord.QueueResolved, ID: coord.ApprovalID(id)}
}
