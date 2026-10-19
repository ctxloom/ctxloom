package tui

import (
	"context"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The split view is the interactive run's whole screen: the agent tree on
// the left, and on the right the selected agent — the session's own engine
// live in one emulated terminal, a delegated child as its feed with an input
// line, or the approvals view when one is open. One owner draws all of it,
// so nothing is layered over a passthrough: no scroll region, no held
// output, no modal on a borrowed screen.
//
// Focus is the pane's by default: every key, paste and mouse event goes to
// the engine (re-encoded for the protocol it asked for) or the child's input
// line. The prefix key moves focus to the tree; Esc, q or the prefix again
// hands it back.

// splitFocus is which side of the split holds the keyboard.
type splitFocus int

const (
	// focusPane: keys go to the right pane — the engine, or a child's input
	// line.
	focusPane splitFocus = iota
	// focusList: keys drive the agent tree.
	focusList
)

const (
	// glyphColumnWidth is the panel on a terminal too narrow for the tree:
	// one glyph per agent and a space.
	glyphColumnWidth = 2
	// minPaneCols is the narrowest the right pane is let go for the full
	// panel; below rosterPaneWidth+1+minPaneCols the panel collapses to the
	// glyph column rather than squeezing the engine.
	minPaneCols = 40
	// colorWait bounds how long the engine's first size waits for the human's
	// terminal to report its background. The engine asks for it (OSC 11) as
	// it starts to choose a light or dark theme, and the first size is what
	// lets it start; past this the emulator's default answers.
	colorWait = 200 * time.Millisecond
)

// Header names the session the split view runs in, at the top of the panel.
type Header struct {
	Harp   string
	Agent  string
	Engine string
	Model  string
}

// SplitOptions are a split view's seams.
type SplitOptions struct {
	// Port is everything the split view reads and does: the roster, a
	// child's feed, the control verbs and the approval queue. Nil shows only
	// the engine, and every action says it is unavailable.
	Port frontend.Port
	// Self is the session's own harp: its row is the engine's terminal.
	Self string
	// Term is the session's interactive terminal. The split view writes the
	// human's input and its emulator's replies to it and sizes it; the
	// caller copies its output into Pane.
	Term frontend.Terminal
	// Prefix moves focus between the pane and the tree.
	Prefix PrefixKey
	// Header names the session at the top of the panel.
	Header Header
	// Present times the approvals focus lock; the zero value is the defaults.
	Present PresentPolicy
	// Clock is the split view's time for its own decisions; nil is real time.
	Clock Clock
	// Notify raises a desktop notification as an approval parks, beside
	// the bell and under its rate limit; nil raises none.
	Notify Notifier
	// In and Out are the human's terminal.
	In  io.Reader
	Out io.Writer
	// NewPane builds the engine's emulated terminal; nil is termemu.NewXVT.
	NewPane func(cols, rows int, toChild io.Writer, cb termemu.Callbacks) termemu.Terminal
	// WindowSize states the columns and rows of an Out bubbletea cannot
	// measure (one that is no tty); zero lets bubbletea measure the
	// terminal, as it does for a tty.
	WindowSize [2]int
}

// Split is the split view's bubbletea model. A pointer: the emulator, the
// feed and the approval queue it holds are shared state, never copied.
type Split struct {
	ctx     context.Context
	port    frontend.Port
	self    string
	term    frontend.Terminal
	prefix  PrefixKey
	header  Header
	present PresentPolicy
	clock   Clock
	notify  Notifier
	pane    termemu.Terminal
	// post delivers a message to the running program from any goroutine: a
	// timer's, the pane's callbacks'.
	post    func(tea.Msg)
	mailbox *paneMailbox
	// driven: a test delivers the standing receives' messages (frames, pane
	// events, run events, feed events) itself, so they are not started.
	driven bool

	w, h int
	// colorsKnown: the human's terminal reported its background (or
	// colorWait passed); until then the engine is not sized.
	colorsKnown bool
	paneW       int // the pane size the engine was last given (0: none yet)
	paneH       int

	focus     splitFocus
	rows      []RosterRow
	sel       string
	collapsed map[string]bool
	runEvents <-chan coord.Event
	// refreshArmed: a debounced roster refresh is already scheduled.
	refreshArmed bool

	frame     termemu.Frame
	paneLines []string
	title     string
	mouseMode termemu.MouseTracking
	scroll    paneScroll

	child childView

	approvalsView
	lastInput time.Time

	status    string
	errMsg    string
	rosterErr string
	banner    string
	note      string
	noteGen   int
}

// newSplit builds the model over a pane that already exists.
func newSplit(ctx context.Context, o SplitOptions, pane termemu.Terminal, post func(tea.Msg), mb *paneMailbox) *Split {
	if o.Clock == nil {
		o.Clock = RealClock{}
	}
	return &Split{
		ctx:       ctx,
		port:      o.Port,
		self:      o.Self,
		term:      o.Term,
		prefix:    o.Prefix,
		header:    o.Header,
		present:   o.Present.Normalized(),
		clock:     o.Clock,
		notify:    o.Notify,
		pane:      pane,
		post:      post,
		mailbox:   mb,
		sel:       o.Self,
		collapsed: map[string]bool{},
		approvalsView: approvalsView{
			apprTick: approvalsTickEvery,
		},
	}
}

// Messages.
type frameMsg struct{}
type colorWaitMsg struct{}

// Init starts the frame pump, the pane's event pump and the roster watch,
// asks the human's terminal for its background, and forces the renderer's
// width method to grapheme clusters.
//
// bubbletea picks grapheme widths only when the human's terminal answers
// DECRQM 2027, and otherwise re-measures each row with wcwidth, which counts
// a VS16, ZWJ, skin-tone or flag emoji narrower than the emulator (and the
// engine) laid it out.
func (s *Split) Init() tea.Cmd {
	s.clock.AfterFunc(colorWait, func() { s.post(colorWaitMsg{}) })
	force := func() tea.Msg { return tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet} }
	return tea.Batch(force, tea.RequestBackgroundColor, s.waitFrame(), s.waitPaneEvents(), s.openRosterCmd())
}

// waitFrame receives the pane's next committed frame.
func (s *Split) waitFrame() tea.Cmd {
	if s.driven {
		return nil
	}
	ready, done := s.pane.Ready(), s.ctx.Done()
	return func() tea.Msg {
		select {
		case <-ready:
			return frameMsg{}
		case <-done:
			return nil
		}
	}
}

// Update dispatches one message.
func (s *Split) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := s.update(msg)
	s.keepScroll()
	return s, cmd
}

func (s *Split) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.key(msg)
	case tea.PasteMsg:
		return s.paste(msg.Content)
	case tea.MouseMsg:
		s.mouse(msg)
		return nil
	case tea.FocusMsg:
		s.pane.Focus(true)
	case tea.BlurMsg:
		s.pane.Focus(false)
	case tea.WindowSizeMsg:
		s.resize(msg.Width, msg.Height)
	case tea.BackgroundColorMsg:
		s.pane.SetDefaultColors(nil, msg.Color)
		s.colorsKnown = true
		s.sizePane()
	case colorWaitMsg:
		s.colorsKnown = true
		s.sizePane()
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeUnicodeCore {
			// The renderer switched width method after its first paint and
			// skips identical views: repaint so the switch shows.
			return tea.ClearScreen
		}
	case frameMsg:
		s.adoptFrame()
		return s.waitFrame()
	case paneEventsMsg:
		return s.applyPaneEvents(msg)
	}
	return s.updateMore(msg)
}

// updateMore dispatches the roster's, the child's, the approvals' and the
// chrome's messages.
func (s *Split) updateMore(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case rosterWatchMsg, rosterEventsMsg, rosterRefreshMsg, rosterMsg, rosterErrMsg:
		return s.applyRosterMsg(msg)
	case feedOpenedMsg, feedErrMsg, feedEventMsg, feedClosedMsg:
		return s.applyChildFeedMsg(msg)
	case controlResultMsg:
		s.applyControlResult(msg)
	case summonMsg, summonRetryMsg, summonCancelMsg, armCheckMsg:
		return s.applySummonMsg(msg)
	case queueEventMsg, approvalsTickMsg, answerResultMsg, revokeResultMsg:
		return s.applyApprovalsMsg(msg)
	case approvalsCountMsg, ringMsg, noteMsg, noteExpiredMsg:
		return s.applyChromeMsg(msg)
	}
	return nil
}

// layout splits the width: the panel (the full tree, or the glyph column on
// a narrow terminal), one divider column, and the pane.
func (s *Split) layout() (panelW, paneW int) {
	panelW = rosterPaneWidth
	if s.w < rosterPaneWidth+1+minPaneCols {
		panelW = glyphColumnWidth
	}
	return panelW, max(s.w-panelW-1, 1)
}

// resize relays the split out for the human's terminal size. The engine is
// sized only once the human's colours are known (sizePane). A zero size is
// a terminal that could not be measured (bubbletea reports one for an
// output that is no tty): it says nothing, so the last size stands.
func (s *Split) resize(w, h int) {
	if w < 1 || h < 1 {
		return
	}
	s.w, s.h = w, h
	s.sizePane()
}

// sizePane gives the emulator and the engine's terminal the pane's size: one
// resize of the emulator, one TIOCSWINSZ on the engine's pty.
func (s *Split) sizePane() {
	if !s.colorsKnown || s.w < 1 || s.h < 1 {
		return
	}
	_, paneW := s.layout()
	if paneW == s.paneW && s.h == s.paneH {
		return
	}
	s.paneW, s.paneH = paneW, s.h
	s.pane.Resize(paneW, s.h)
	if s.term != nil {
		_ = s.term.Resize(uint16(s.h), uint16(paneW))
	}
}

// adoptFrame takes the pane's latest committed frame and serializes each of
// its rows once; every view until the next frame reuses them.
func (s *Split) adoptFrame() {
	f := s.pane.Frame()
	if f.Seq == s.frame.Seq {
		return
	}
	s.frame = f
	if f.Buf == nil {
		s.paneLines = nil
		return
	}
	lines := make([]string, f.Buf.Height())
	for y := range lines {
		lines[y] = f.Line(y)
	}
	s.paneLines = lines
}

// key routes one key press: to the approvals view while it is open, the
// prefix to the focus toggle, then to whichever side has focus.
func (s *Split) key(msg tea.KeyPressMsg) tea.Cmd {
	s.lastInput = s.clock.Now()
	if s.apprOpen {
		return s.approvalKey(msg)
	}
	if s.prefix.Matches(msg) {
		s.toggleFocus()
		return nil
	}
	if s.scroll.on {
		s.scrollKey(msg)
		return nil
	}
	if s.focus == focusList {
		return s.listKey(msg)
	}
	if s.showingTerminal() {
		s.pane.SendKey(uv.KeyPressEvent(msg))
		return nil
	}
	return s.childKey(msg)
}

func (s *Split) toggleFocus() {
	if s.focus == focusList {
		s.focus = focusPane
		return
	}
	s.focus = focusList
	s.banner = ""
}

// paste routes one bracketed paste, whole, like a key.
func (s *Split) paste(text string) tea.Cmd {
	s.lastInput = s.clock.Now()
	switch {
	case s.apprOpen:
		s.approvalPaste(text)
	case s.focus == focusList, s.scroll.on:
	case s.showingTerminal():
		s.pane.Paste(text)
	default:
		s.child.text += text
	}
	return nil
}

// mouse forwards a mouse event over the engine's pane, in pane coordinates,
// when the engine asked for mouse reports; anything over the panel or the
// divider goes nowhere. While scrolling it is the scroll's.
func (s *Split) mouse(msg tea.MouseMsg) {
	if s.scroll.on {
		s.scrollMouse(msg)
		return
	}
	if s.apprOpen || !s.showingTerminal() {
		return
	}
	panelW, _ := s.layout()
	m := msg.Mouse()
	m.X -= panelW + 1
	if m.X < 0 {
		return
	}
	um := uv.Mouse(m)
	switch msg.(type) {
	case tea.MouseClickMsg:
		s.pane.SendMouse(uv.MouseClickEvent(um))
	case tea.MouseReleaseMsg:
		s.pane.SendMouse(uv.MouseReleaseEvent(um))
	case tea.MouseWheelMsg:
		s.pane.SendMouse(uv.MouseWheelEvent(um))
	case tea.MouseMotionMsg:
		s.pane.SendMouse(uv.MouseMotionEvent(um))
	}
}

// showingTerminal reports whether the right pane is the engine's terminal:
// the session's own row is selected.
func (s *Split) showingTerminal() bool { return s.sel == s.self }

// View renders the split.
func (s *Split) View() tea.View {
	v := tea.NewView(strings.Join(s.render(), "\n"))
	v.AltScreen = true
	v.ReportFocus = true
	v.WindowTitle = s.title
	switch {
	case s.scroll.on:
		v.MouseMode = tea.MouseModeCellMotion
	case s.showingTerminal() && !s.apprOpen:
		v.MouseMode = teaMouseMode(s.mouseMode)
	}
	v.Cursor = s.cursor()
	return v
}

// teaMouseMode is the renderer's mouse mode for the engine's tracking: the
// split view asks the human's terminal for exactly what the engine asked of
// its own, so with none the human's terminal keeps native selection.
func teaMouseMode(m termemu.MouseTracking) tea.MouseMode { return teaMouseModes[m] }

// teaMouseModes maps the engine's tracking onto the renderer's modes; no
// tracking is the zero mode, MouseModeNone.
var teaMouseModes = map[termemu.MouseTracking]tea.MouseMode{
	termemu.MouseClicks: tea.MouseModeCellMotion,
	termemu.MouseDrags:  tea.MouseModeCellMotion,
	termemu.MouseMotion: tea.MouseModeAllMotion,
}

// cursor is the human's cursor: the engine's, offset into the pane, or the
// end of a child's input line; none while the tree or the approvals view has
// focus, or the pane shows history.
func (s *Split) cursor() *tea.Cursor {
	if s.apprOpen || s.focus == focusList || s.scroll.on || s.h < 1 {
		return nil
	}
	panelW, _ := s.layout()
	if s.showingTerminal() {
		if !s.frame.CursorVisible {
			return nil
		}
		return tea.NewCursor(s.frame.Cursor.X+panelW+1, s.frame.Cursor.Y)
	}
	return tea.NewCursor(panelW+1+s.inputCursor(), s.h-1)
}

// render draws exactly h rows of exactly w columns: the panel, the divider,
// the pane — none before the first size.
func (s *Split) render() []string {
	panelW, paneW := s.layout()
	panel := s.panelLines(panelW, s.h)
	right := s.rightLines(paneW, s.h)
	out := make([]string, s.h)
	for y := range out {
		out[y] = padCell(panel[y], panelW) + ansi.ResetStyle + "│" + right[y]
	}
	return out
}

// rightLines are the pane's h rows, each exactly w columns.
func (s *Split) rightLines(w, h int) []string {
	switch {
	case s.apprOpen:
		return fitRows(s.appr.render(w, h, apprChrome{arming: s.arming, armDiscarded: s.armDiscarded}), w, h)
	case s.scroll.on:
		return s.scrollLines(w, h)
	case s.showingTerminal():
		return s.terminalLines(w, h)
	}
	return s.childLines(w, h)
}

// terminalLines are the engine's frame rows. They are already the pane's
// width once the engine has been sized; until then they are fitted.
func (s *Split) terminalLines(w, h int) []string {
	if s.frame.Buf != nil && s.frame.Buf.Width() == w && len(s.paneLines) >= h {
		return s.paneLines[:h]
	}
	return fitRows(s.paneLines, w, h)
}

// fitRows pads or cuts rows to exactly h rows of exactly w columns.
func fitRows(rows []string, w, h int) []string {
	out := make([]string, h)
	blank := strings.Repeat(" ", w)
	for y := range out {
		if y < len(rows) {
			out[y] = padCell(rows[y], w)
		} else {
			out[y] = blank
		}
	}
	return out
}
