package tui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/displaysafe"
)

// Approvals in the split view keep their focus locks. A summoned
// approval takes the right pane only at a pause in the human's typing
// (PresentPolicy.QuietFor), so a key typed for the engine lands in the
// engine; once shown it is inert for PresentPolicy.ArmFor, restarting on
// every key and counting what it swallowed, so a key already in flight
// cannot answer it; it never takes focus from a tree the human is using —
// that surface is told who asked instead; and inside it the approvals
// model's own lock (no letter or digit decides) holds as everywhere.

// approvalBellInterval rate-limits the arrival bell and the desktop alert
// beside it: a burst of requests rings and alerts once.
const approvalBellInterval = 10 * time.Second

// alertTitle heads the desktop notification of an approval waiting.
const alertTitle = "ctxloom: approval waiting"

// Notifier raises a notification on the human's desktop. It may block; the
// split view calls it off its own loop.
type Notifier interface {
	Notify(title, body string)
}

// ErrEngaged refuses a summon while the human is using the split view's
// tree or already has the approvals view open: focus never moves from a
// surface in use. The surface was told the notice.
var ErrEngaged = errors.New("tui: the split view is in use; the notice was shown instead")

// ErrUnavailable refuses a summon the split view cannot show: it has
// closed, or the session has no approval queue.
var ErrUnavailable = errors.New("tui: the split view is unavailable")

// approvalsUnavailable is why the approvals view cannot open.
const approvalsUnavailable = "approvals unavailable (no coordinator for this session)"

// errSummonReplaced ends a summon a newer one took the place of.
var errSummonReplaced = errors.New("tui: summon replaced by a newer one")

// approvalsView is the split view's approvals state.
type approvalsView struct {
	appr     approvalsModel
	apprOpen bool
	// apprSummoned: opened by a summon (focus returns to the pane), not by
	// the tree's a (focus returns to the tree).
	apprSummoned bool
	// apprEvents is the one queue subscription, held for the split view's
	// life; its pump runs whether or not the view is open.
	apprEvents <-chan coord.QueueEvent
	apprTick   time.Duration

	arming       bool
	armUntil     time.Time
	armGen       int
	armDiscarded int

	summon *summonReq

	pendingN      int
	pendingOldest time.Time
	lastBell      time.Time
}

// summonReq is one Summon waiting to be shown.
type summonReq struct {
	id     uint64
	notice string
	reply  chan<- error // buffered: the model never waits on a caller
}

type (
	summonMsg       summonReq
	summonRetryMsg  struct{ id uint64 }
	summonCancelMsg struct{ id uint64 }
	armCheckMsg     struct{ gen int }
)

// Chrome messages from the split view's callers (SplitView).
type (
	approvalsCountMsg struct {
		n       int
		oldest  time.Time
		arrived bool
	}
	ringMsg struct{}
	noteMsg struct {
		text string
		d    time.Duration
	}
	noteExpiredMsg struct{ gen int }
)

// engaged reports whether the human is using the split view's own surface:
// the tree, an approvals view already open, or the engine's history.
func (s *Split) engaged() bool { return s.focus == focusList || s.apprOpen || s.scroll.on }

func (s *Split) applySummonMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case summonMsg:
		if s.summon != nil {
			s.summon.reply <- errSummonReplaced
		}
		req := summonReq(msg)
		s.summon = &req
		return s.trySummon()
	case summonRetryMsg:
		if s.summon != nil && s.summon.id == msg.id {
			return s.trySummon()
		}
	case summonCancelMsg:
		if s.summon != nil && s.summon.id == msg.id {
			s.summon = nil
		}
	case armCheckMsg:
		s.armCheck(msg.gen)
	}
	return nil
}

// trySummon makes one attempt at the waiting summon: refused with the
// notice shown when the surface is in use, shown once the human's input has
// been quiet for QuietFor, otherwise retried when it will have been.
func (s *Split) trySummon() tea.Cmd {
	req := s.summon
	if s.engaged() {
		s.summon = nil
		s.banner = pendingGlyph + " " + displaysafe.Text(req.notice, true) + " — a to review"
		req.reply <- ErrEngaged
		return nil
	}
	// With no input yet, the quiet is as long as the clock's age.
	if quiet := s.clock.Now().Sub(s.lastInput); quiet < s.present.QuietFor {
		id := req.id
		s.clock.AfterFunc(s.present.QuietFor-quiet, func() { s.post(summonRetryMsg{id: id}) })
		return nil
	}
	s.summon = nil
	cmd := s.openApprovals(true)
	if !s.apprOpen {
		req.reply <- ErrUnavailable
		return nil
	}
	req.reply <- nil
	return cmd
}

// openApprovals opens the approvals view in the right pane. Summoned, it is
// inert for ArmFor first.
func (s *Split) openApprovals(summoned bool) tea.Cmd {
	src := s.approvalSource()
	if src == nil {
		s.reportErr(approvalsUnavailable)
		return nil
	}
	var cmds []tea.Cmd
	if s.apprEvents == nil {
		s.apprEvents = src.Subscribe(s.ctx)
		cmds = append(cmds, waitQueueCmd(s.apprEvents))
	}
	s.apprOpen, s.apprSummoned, s.banner = true, summoned, ""
	s.appr = newApprovalsModel(src, s.clock.Now, s.prefix.keystroke)
	s.appr.tickEvery = s.apprTick
	s.appr.events = s.apprEvents
	s.appr.resync()
	cmds = append(cmds, s.appr.tick())
	// The inert window's first check is ArmFor away; a key in the window
	// moves its end (discardWhileArming), and the check follows it.
	s.arming, s.armDiscarded = summoned, 0
	if summoned {
		s.armAt(s.present.ArmFor)
	}
	return tea.Batch(cmds...)
}

func (s *Split) approvalSource() coord.ApprovalSource {
	if s.port == nil {
		return nil
	}
	return s.port.Approvals()
}

// armAt checks the inert window again after d.
func (s *Split) armAt(d time.Duration) {
	gen := s.armGen
	s.clock.AfterFunc(d, func() { s.post(armCheckMsg{gen: gen}) })
}

// armCheck ends the inert window, or re-schedules the check when a key
// typed during it moved its end.
func (s *Split) armCheck(gen int) {
	if gen != s.armGen {
		return
	}
	if now := s.clock.Now(); now.Before(s.armUntil) {
		s.armAt(s.armUntil.Sub(now))
		return
	}
	s.arming = false
}

// approvalKey is a key while the approvals view is open: swallowed while it
// is inert, otherwise the approvals model's.
func (s *Split) approvalKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.arming {
		s.discardWhileArming()
		return nil
	}
	next, cmd, hide := s.appr.key(msg)
	s.appr = next
	if hide {
		s.closeApprovals()
		return nil
	}
	return cmd
}

func (s *Split) approvalPaste(text string) {
	if s.arming {
		s.discardWhileArming()
		return
	}
	s.appr = s.appr.paste(text)
}

func (s *Split) discardWhileArming() {
	s.armDiscarded++
	s.armUntil = s.clock.Now().Add(s.present.ArmFor)
}

// closeApprovals hides the approvals view. Focus is where the view found
// it — the pane for a summon (it never takes focus from the tree), the tree
// for its a — and an inert window's pending check is retired.
func (s *Split) closeApprovals() {
	s.apprOpen, s.arming = false, false
	s.armGen++
}

// applyApprovalsMsg lands the queue's and the decisions' messages. The one
// queue pump re-arms itself whether or not the view is open; the view closes
// itself once its list is empty.
func (s *Split) applyApprovalsMsg(msg tea.Msg) tea.Cmd {
	if q, ok := msg.(queueEventMsg); ok && !s.apprOpen {
		if !q.ok {
			return nil
		}
		return waitQueueCmd(s.apprEvents)
	}
	if !s.apprOpen {
		return nil
	}
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case queueEventMsg:
		s.appr, cmd = s.appr.applyQueueEvent(msg)
	case approvalsTickMsg:
		s.appr, cmd = s.appr.applyTick()
	case answerResultMsg:
		s.appr = s.appr.applyAnswerResult(msg)
	case revokeResultMsg:
		s.appr = s.appr.applyRevokeResult(msg)
	}
	if s.appr.done() {
		s.closeApprovals()
		s.reportOK("no approvals waiting")
	}
	return cmd
}

// applyChromeMsg lands what the split view's callers tell it: the approval
// count (and the arrival bell), a bell, a timed note.
func (s *Split) applyChromeMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case approvalsCountMsg:
		s.pendingN, s.pendingOldest = msg.n, msg.oldest
		now := s.clock.Now()
		if msg.arrived && !s.apprOpen && now.Sub(s.lastBell) >= approvalBellInterval {
			s.lastBell = now
			return tea.Batch(tea.Raw("\a"), s.alert(msg.n))
		}
	case ringMsg:
		return tea.Raw("\a")
	case noteMsg:
		s.noteGen++
		s.note = msg.text
		gen := s.noteGen
		s.clock.AfterFunc(msg.d, func() { s.post(noteExpiredMsg{gen: gen}) })
	case noteExpiredMsg:
		if msg.gen == s.noteGen {
			s.note = ""
		}
	}
	return nil
}

// alert raises the desktop notification for n approvals waiting in this
// session, off the program's loop; none without a notifier.
func (s *Split) alert(n int) tea.Cmd {
	if s.notify == nil {
		return nil
	}
	notify, body := s.notify, s.self+": "+approvalsCount(n)+" waiting"
	return func() tea.Msg {
		notify.Notify(alertTitle, body)
		return nil
	}
}
