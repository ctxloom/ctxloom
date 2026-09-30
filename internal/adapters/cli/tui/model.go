package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// rosterRefreshEvery is the roster pane's refresh cadence while the overlay
// is open (the surround bar polls independently).
const rosterRefreshEvery = 2 * time.Second

const (
	focusRoster = iota
	focusFeed
)

// rosterPaneWidth is the agents pane's fixed column budget.
const rosterPaneWidth = 26

// Model is the overlay's bubbletea model. All I/O rides Sources and the
// copy sink, so tests drive Update directly.
type Model struct {
	src       Sources
	ctx       context.Context // parent for watches; overlay-scoped
	geo       termui.OverlayGeometry
	prefixKey string // tea key name of the prefix (e.g. "ctrl+]")

	full     bool // full-screen (alt) vs quick panel
	firstKey bool // next key may be the presentation chord (f = full screen)
	focus    int

	rows []RosterRow
	sel  int

	feedHarp   string
	feedSource string
	feed       *Feed
	items      []feedItem
	expanded   map[int]bool
	cursor     int
	follow     bool
	vp         viewport.Model

	composeVerb string // the open input line's control verb ("" = closed): keys type into it
	composeHarp string // the explicit target, latched when the line opens
	composeText string

	// The hint bar carries one line, chosen in this order: the last action's
	// failure, the roster's own failure, then status. Each slot is owned by
	// exactly one producer so that none of them outlives its subject:
	// rosterErr is retired by the next successful fetch, and an action
	// replaces both of its own slots at once.
	status    string // the last action's outcome, or a roster-owned note
	errMsg    string // the last action's failure
	rosterErr string // the roster fetch's failure, while it persists

	// approvals: the approvals view is open (summoned, or the a key), and
	// holds the keymap.
	approvals bool
	appr      approvalsModel
	// summoned: termui opened this overlay for an approval. It owns the whole
	// screen from the first frame and is inert until armed.
	summoned     bool
	arming       bool
	armDiscarded int
	// banner is an approval that arrived while the roster view was open
	// (Overlay.Notify): the human is told, and focus stays where it is.
	banner string
}

// The roster pane owns these two status lines. It refreshes every
// rosterRefreshEvery, so it may only ever clear a line it wrote itself —
// anything else it clears has a lifetime of at most that tick.
const (
	statusLoading    = "loading agents…"
	statusNoSessions = "no observable sessions"
)

// reportOK and reportErr record an action's outcome. An action produces
// exactly one outcome, so each writes BOTH slots: otherwise an earlier
// failure outlives the success that superseded it and — since View prefers
// errMsg — hides it.
func (m *Model) reportOK(s string)  { m.status, m.errMsg = s, "" }
func (m *Model) reportErr(s string) { m.status, m.errMsg = "", s }

// hintNote picks the single line the hint bar carries.
func (m Model) hintNote() string {
	switch {
	case m.errMsg != "":
		return m.errMsg
	case m.rosterErr != "":
		return m.rosterErr
	default:
		return m.status
	}
}

// Messages.
type rosterMsg struct{ rows []RosterRow }
type rosterErrMsg struct{ err error }
type rosterTickMsg struct{}
type feedOpenedMsg struct {
	harp string
	feed *Feed
}
type feedErrMsg struct {
	harp string
	err  error
}
type feedEventMsg struct {
	harp string
	ev   operations.SessionFeedEvent
}
type feedClosedMsg struct {
	harp string
	err  error
}
type controlResultMsg struct {
	req coord.ControlRequest
	res coord.ControlResult
	err error
}

// NewModel builds the overlay model. prefixByte is the interceptor's key;
func NewModel(ctx context.Context, src Sources, geo termui.OverlayGeometry, prefixByte byte) Model {
	m := Model{
		src:       src,
		ctx:       ctx,
		geo:       geo,
		prefixKey: teaKeyName(prefixByte),
		firstKey:  true,
		follow:    true,
		expanded:  map[int]bool{},
		status:    statusLoading,
	}
	m.vp = viewport.New(viewport.WithWidth(m.feedWidth()), viewport.WithHeight(m.contentHeight()))
	return m
}

// teaKeyName maps a raw control byte onto bubbletea's key-name vocabulary so
// the model recognizes the configured prefix as "back".
func teaKeyName(b byte) string {
	switch {
	case b >= 1 && b <= 26:
		return "ctrl+" + string(rune('a'+b-1))
	case b >= 28 && b <= 31: // \ ] ^ _
		return "ctrl+" + string(rune('['+b-27))
	case b == 127:
		return "backspace"
	default:
		return "ctrl+@"
	}
}

// Layout: one header line + content + one hint line.
func (m Model) totalHeight() int {
	if m.full {
		return m.geo.Rows
	}
	return m.geo.PanelRows
}
func (m Model) contentHeight() int { return max(m.totalHeight()-2, 1) }
func (m Model) feedWidth() int     { return max(m.geo.Cols-rosterPaneWidth-1, 20) }

// Init starts the roster fetch and its refresh tick — or, for a summoned
// modal, only the approvals view's queue pump and tick: the modal shows no
// roster.
func (m Model) Init() tea.Cmd {
	if m.summoned {
		return m.appr.initCmds()
	}
	cmds := []tea.Cmd{m.fetchRosterCmd(), rosterTick()}
	if m.approvals {
		cmds = append(cmds, m.appr.initCmds())
	}
	return tea.Batch(cmds...)
}

func (m Model) fetchRosterCmd() tea.Cmd {
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		rows, err := src.Roster(ctx)
		if err != nil {
			return rosterErrMsg{err}
		}
		return rosterMsg{rows}
	}
}

func rosterTick() tea.Cmd {
	return tea.Tick(rosterRefreshEvery, func(time.Time) tea.Msg { return rosterTickMsg{} })
}

// waitEventCmd re-arms per event: one channel receive per command keeps the
// feed pump inside bubbletea's message loop.
func waitEventCmd(harp string, f *Feed) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-f.Events
		if !ok {
			var err error
			select {
			case err = <-f.Errs:
			default:
			}
			return feedClosedMsg{harp: harp, err: err}
		}
		return feedEventMsg{harp: harp, ev: ev}
	}
}

// openFeed cancels the current watch and opens the newly selected harp's.
//
// It mutates the receiver, so a caller must complete the call before the model
// value it returns is taken. `return m, m.openFeed(...)` does not: Go orders
// the function calls within a return statement, but not a plain operand
// against them, so whether the returned Model is the one openFeed just reset
// is left to the compiler. Bind the command to a variable first.
func (m *Model) openFeed(harp string) tea.Cmd {
	if m.feed != nil && m.feed.Cancel != nil {
		m.feed.Cancel()
	}
	m.feed = nil
	m.feedHarp = harp
	m.feedSource = ""
	m.items = nil
	m.expanded = map[int]bool{}
	m.cursor = 0
	m.follow = true
	// The pane's notes describe the feed being replaced ("feed ended", a
	// watch error): they do not survive the switch.
	m.reportOK("")
	m.refreshFeed()
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		f, err := src.Watch(ctx, harp)
		if err != nil {
			return feedErrMsg{harp: harp, err: err}
		}
		return feedOpenedMsg{harp: harp, feed: f}
	}
}

// Update dispatches one message. Every arm's handling lives in its own
// method: the dispatch stays readable as the message family grows, and each
// arm can be reasoned about (and read) on its own.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	// KeyPressMsg, not the KeyMsg interface: bubbletea v2 splits press from
	// release, and matching the interface would run every binding twice on a
	// terminal that reports releases.
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case rosterMsg:
		return m.applyRoster(msg)
	case rosterErrMsg:
		m.rosterErr = fmt.Sprintf("roster: %v", msg.err)
		return m, nil
	case rosterTickMsg:
		return m, tea.Batch(m.fetchRosterCmd(), rosterTick())
	case feedOpenedMsg, feedErrMsg, feedEventMsg, feedClosedMsg:
		return m.applyFeedMsg(msg)
	case controlResultMsg:
		return m.applyControlResult(msg)
	case geometryMsg:
		return m.applyGeometry(termui.OverlayGeometry(msg))
	case armedMsg, noticeMsg:
		return m.applyChromeMsg(msg), nil
	case tea.PasteMsg, queueEventMsg, approvalsTickMsg, answerResultMsg, revokeResultMsg:
		return m.applyApprovalsMsg(msg)
	}
	return m, nil
}

func (m Model) applyFeedMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case feedOpenedMsg:
		return m.applyFeedOpened(msg)
	case feedErrMsg:
		return m.applyFeedErr(msg)
	case feedEventMsg:
		return m.applyFeedEvent(msg)
	case feedClosedMsg:
		return m.applyFeedClosed(msg)
	}
	return m, nil
}

// geometryMsg carries a new terminal geometry into a running overlay
// (Overlay.Resize).
type geometryMsg termui.OverlayGeometry

// applyGeometry relays the model out and tells the renderer the size it now
// owns — the panel's height, or the whole drawable area in full screen —
// the same way the switch to full screen does (ownTerminalCmd).
func (m Model) applyGeometry(geo termui.OverlayGeometry) (tea.Model, tea.Cmd) {
	m.geo = geo
	m.resize()
	w, h := geo.Cols, m.totalHeight()
	return m, func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
}

func (m Model) applyFeedErr(msg feedErrMsg) (tea.Model, tea.Cmd) {
	if msg.harp == m.feedHarp {
		m.errMsg = fmt.Sprintf("feed %s: %v", msg.harp, msg.err)
	}
	return m, nil
}

// applyControlResult lands one control round trip's outcome on the hint bar.
// An ask's answer is flattened onto that one line: the full reply is also in
// the target's own feed, as the agent_send that carried it.
func (m Model) applyControlResult(msg controlResultMsg) (tea.Model, tea.Cmd) {
	harp := msg.req.Harp
	if msg.err != nil {
		m.reportErr(fmt.Sprintf("%s %s: %v", controlLabel(msg.req.Verb), harp, msg.err))
		return m, nil
	}
	switch msg.req.Verb {
	case coord.ControlVerbSteer:
		m.reportOK(fmt.Sprintf("injected into %s: %s", harp, msg.res.Delivery))
	case coord.ControlVerbQuestion, coord.ControlVerbSummarize:
		what := "answer"
		if msg.req.Verb == coord.ControlVerbSummarize {
			what = "summary"
		}
		text := ""
		if msg.res.Answer != nil {
			text = strings.Join(strings.Fields(msg.res.Answer.Text), " ")
		}
		m.reportOK(fmt.Sprintf("%s from %s: %s", what, harp, text))
	case coord.ControlVerbPause:
		m.reportOK(changedOr(msg.res.Changed, "paused "+harp, harp+" was already paused"))
	case coord.ControlVerbResume:
		m.reportOK(changedOr(msg.res.Changed, "resumed "+harp, harp+" was not paused"))
	}
	return m, nil
}

func changedOr(changed bool, did, didNot string) string {
	if changed {
		return did
	}
	return didNot
}

// applyRoster adopts a refreshed roster, keeping the selection on the harp it
// was on. The FIRST roster to arrive also opens the selection's feed.
func (m Model) applyRoster(msg rosterMsg) (tea.Model, tea.Cmd) {
	hadRows := len(m.rows) > 0
	keep := ""
	if hadRows && m.sel < len(m.rows) {
		keep = m.rows[m.sel].Harp
	}
	m.rows = msg.rows
	m.sel = 0
	for i, r := range m.rows {
		if r.Harp == keep {
			m.sel = i
			break
		}
	}
	m.rosterErr = ""
	if len(m.rows) == 0 {
		m.status = statusNoSessions
		return m, nil
	}
	if m.status == statusLoading || m.status == statusNoSessions {
		m.status = ""
	}
	if hadRows {
		return m, nil
	}
	cmd := m.openFeed(m.rows[m.sel].Harp)
	return m, cmd
}

func (m Model) applyFeedOpened(msg feedOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.harp != m.feedHarp {
		// Stale open (selection moved on): release it.
		if msg.feed.Cancel != nil {
			msg.feed.Cancel()
		}
		return m, nil
	}
	m.feed = msg.feed
	m.feedSource = msg.feed.Source
	return m, waitEventCmd(msg.harp, msg.feed)
}

func (m Model) applyFeedEvent(msg feedEventMsg) (tea.Model, tea.Cmd) {
	if msg.harp != m.feedHarp || m.feed == nil {
		return m, nil
	}
	if add := itemsFromFeedEvent(msg.ev); len(add) > 0 {
		m.items = append(m.items, add...)
		if m.follow {
			m.cursor = len(m.items) - 1
		}
		m.refreshFeed()
	}
	return m, waitEventCmd(msg.harp, m.feed)
}

func (m Model) applyFeedClosed(msg feedClosedMsg) (tea.Model, tea.Cmd) {
	if msg.harp != m.feedHarp {
		return m, nil
	}
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("feed ended: %v", msg.err)
	} else {
		m.status = "feed ended (agent exited)"
	}
	m.feed = nil
	return m, nil
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.approvals {
		return m.updateApprovalsKey(msg)
	}
	if m.composeVerb != "" {
		return m.updateComposeKey(msg)
	}
	key := msg.String()
	if m.firstKey {
		m.firstKey = false
		switch key {
		case "f": // prefix-then-f: full screen
			if m.geo.EngineOnAltScreen {
				m.reportErr(fullScreenUnavailable)
				return m, nil
			}
			m.full = true
			m.resize()
			// v2 has no EnterAltScreen command: the alt screen is a property
			// of the View the model returns (see View), so it follows m.full
			// and the renderer enters and leaves to match. The renderer must
			// still be TOLD the overlay now owns the whole terminal — it was
			// started at the quick panel's height, which is all the overlay
			// owns until this key.
			return m, m.ownTerminalCmd()
		case m.prefixKey:
			// Belt and braces: the interceptor normally converts this into
			// the literal-abort path before the model ever sees it.
			return m.quit()
		}
	}
	switch key {
	case "q", m.prefixKey, "ctrl+c":
		return m.quit()
	case "j", "down":
		return m.moveDown()
	case "k", "up":
		return m.moveUp()
	case "enter":
		if m.focus == focusRoster && len(m.rows) > 0 {
			m.focus = focusFeed
		}
		return m, nil
	case "h", "left":
		m.focus = focusRoster
		return m, nil
	case "tab":
		m.focus = 1 - m.focus
		return m, nil
	case "f":
		m.follow = !m.follow
		if m.follow && len(m.items) > 0 {
			m.cursor = len(m.items) - 1
			m.refreshFeed()
		}
		return m, nil
	case "x":
		if len(m.items) > 0 {
			m.expanded[m.cursor] = !m.expanded[m.cursor]
			m.refreshFeed()
		}
		return m, nil
	case "g":
		m.follow = false
		m.cursor = 0
		m.refreshFeed()
		m.vp.GotoTop()
		return m, nil
	case "G":
		m.follow = true
		if len(m.items) > 0 {
			m.cursor = len(m.items) - 1
		}
		m.refreshFeed()
		return m, nil
	case "i", "?", "s", "p", "r":
		return m.openControl(controlKeys[key])
	case "a":
		return m.openApprovals(false)
	}
	return m, nil
}

// controlKeys maps each control key to the verb it drives.
var controlKeys = map[string]string{
	"i": coord.ControlVerbSteer,
	"?": coord.ControlVerbQuestion,
	"s": coord.ControlVerbSummarize,
	"p": coord.ControlVerbPause,
	"r": coord.ControlVerbResume,
}

// controlLabel is the verb's name on the viewer's surface. Steer keeps the
// viewer's own word for it, "inject".
func controlLabel(verb string) string {
	switch verb {
	case coord.ControlVerbSteer:
		return "inject"
	case coord.ControlVerbQuestion:
		return "ask"
	}
	return verb
}

// controlTakesBody reports whether verb carries typed text (the instruction,
// the question, the summary's focus) and so opens the input line; pause and
// resume act at the keypress.
func controlTakesBody(verb string) bool {
	return verb != coord.ControlVerbPause && verb != coord.ControlVerbResume
}

// openControl starts a control action on the viewed harp: the input line for
// a verb that carries text, the request itself for one that does not. The
// target is latched here so a roster refresh can't silently retarget it.
func (m Model) openControl(verb string) (tea.Model, tea.Cmd) {
	if m.feedHarp == "" {
		m.reportOK("no agent selected to " + controlLabel(verb))
		return m, nil
	}
	if m.src.Control == nil {
		m.reportErr(controlLabel(verb) + " unavailable (no agent bus for this session)")
		return m, nil
	}
	if !controlTakesBody(verb) {
		return m.sendControl(coord.ControlRequest{Verb: verb, Harp: m.feedHarp})
	}
	m.composeVerb = verb
	m.composeHarp = m.feedHarp
	m.composeText = ""
	m.reportOK("")
	return m, nil
}

// updateComposeKey owns every key while the input line is open: printable
// keys type into the line (including j/k — navigation is suspended), enter
// sends over the bus, esc cancels. The engine prefix and ctrl+c still back
// out of the overlay entirely.
func (m Model) updateComposeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// v2 reports printable input as Key.Text — one field covering both v1's
	// KeyRunes and its separate KeySpace. A non-printable key (enter, esc,
	// ctrl+c, the arrows) carries an empty Text and falls through to the
	// bindings below, which is the same precedence v1's type switch had.
	if msg.Text != "" {
		m.composeText += msg.Text
		return m, nil
	}
	switch msg.String() {
	case m.prefixKey, "ctrl+c":
		return m.quit()
	case "esc":
		m.composeVerb = ""
		m.composeText = ""
		return m, nil
	case "enter":
		if strings.TrimSpace(m.composeText) == "" {
			return m, nil
		}
		req := coord.ControlRequest{Verb: m.composeVerb, Harp: m.composeHarp, Body: m.composeText}
		m.composeVerb = ""
		m.composeText = ""
		return m.sendControl(req)
	case "backspace":
		if r := []rune(m.composeText); len(r) > 0 {
			m.composeText = string(r[:len(r)-1])
		}
	}
	return m, nil
}

// sendControl runs the bus round trip off the update loop; the outcome
// renders inline on arrival. It rides the overlay's context: closing the
// viewer abandons a pending ask (the question stays in the target's spool,
// and a late answer is dropped), rather than leaving a waiter nobody reads.
func (m Model) sendControl(req coord.ControlRequest) (tea.Model, tea.Cmd) {
	m.status = controlPending(req)
	control, ctx := m.src.Control, m.ctx
	return m, func() tea.Msg {
		res, err := control(ctx, req)
		return controlResultMsg{req: req, res: res, err: err}
	}
}

// controlPending is the hint while a request is in flight. An ask says it
// waits for a reply: it resolves when the target answers, not when the
// request lands.
func controlPending(req coord.ControlRequest) string {
	switch req.Verb {
	case coord.ControlVerbSteer:
		return "injecting into " + req.Harp + "…"
	case coord.ControlVerbQuestion, coord.ControlVerbSummarize:
		return "asked " + req.Harp + "; waiting for its reply…"
	case coord.ControlVerbPause:
		return "pausing " + req.Harp + "…"
	}
	return "resuming " + req.Harp + "…"
}

// fullScreenUnavailable is why prefix-then-f stays in the panel over an
// engine that is itself on the alternate screen (termui.OverlayGeometry).
const fullScreenUnavailable = "full screen unavailable: the engine is using the alternate screen"

func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.feed != nil && m.feed.Cancel != nil {
		m.feed.Cancel()
	}
	return m, tea.Quit
}

func (m Model) moveDown() (tea.Model, tea.Cmd) {
	if m.focus == focusRoster {
		if m.sel < len(m.rows)-1 {
			m.sel++
			cmd := m.openFeed(m.rows[m.sel].Harp)
			return m, cmd
		}
		return m, nil
	}
	if m.cursor < len(m.items)-1 {
		m.cursor++
		m.refreshFeed()
	}
	return m, nil
}

func (m Model) moveUp() (tea.Model, tea.Cmd) {
	if m.focus == focusRoster {
		if m.sel > 0 {
			m.sel--
			cmd := m.openFeed(m.rows[m.sel].Harp)
			return m, cmd
		}
		return m, nil
	}
	if m.cursor > 0 {
		m.cursor--
		m.follow = false // scrolling back leaves the tail
		m.refreshFeed()
	}
	return m, nil
}

// refreshFeed re-renders the feed viewport and keeps the cursor visible
// (bottom-pinned in follow mode).
func (m *Model) refreshFeed() {
	lines, first := renderItems(m.items, m.feedWidth(), m.expanded, m.cursor)
	m.vp.SetContent(strings.Join(lines, "\n"))
	if m.follow {
		m.vp.GotoBottom()
		return
	}
	if m.cursor < len(first) {
		top := first[m.cursor]
		if top < m.vp.YOffset() {
			m.vp.SetYOffset(top)
		} else if top >= m.vp.YOffset()+m.vp.Height() {
			m.vp.SetYOffset(top - m.vp.Height() + 1)
		}
	}
}

func (m *Model) resize() {
	m.vp.SetWidth(m.feedWidth())
	m.vp.SetHeight(m.contentHeight())
	m.refreshFeed()
}

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
)

// View renders exactly totalHeight lines of at most geo.Cols cells: header,
// roster│feed content, hints. The overlay paints over a live engine session
// and the controller has cleared exactly that many rows for it, so an extra
// line — or a line the terminal wraps because it is too wide — lands on a row
// nothing will repaint. The budget therefore governs: the content rows take
// what the header and hints leave, and both of those are dropped in turn
// rather than allowed to overflow.
// View wraps the rendered panel in a bubbletea v2 View. The alt screen is
// carried here rather than entered by a command (v1's tea.EnterAltScreen):
// in v2 it is a property of the view, so it tracks m.full and the renderer
// enters and leaves to match — including on quit, which is what restores the
// engine's screen underneath.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	// A summoned modal is drawn on the screen termui already took for it
	// (termui's takeScreen): entering an alternate screen of its own would,
	// over an engine on the alternate screen, take the engine off it.
	v.AltScreen = m.full && !m.summoned
	return v
}

// ownTerminalCmd resizes the renderer to the whole terminal, for the switch
// from the quick panel to full screen.
//
// bubbletea resizes on any WindowSizeMsg it receives, including one a command
// produces (tea.go: `case WindowSizeMsg: p.renderer.resize(...)`), which is how
// a mode change decided inside the program reaches the renderer. The program is
// started at the PANEL's height deliberately: until this key the overlay owns
// only the bottom rows, and a renderer told it owns more would paint over the
// engine output the controller is holding above them.
func (m Model) ownTerminalCmd() tea.Cmd {
	geo := m.geo
	return func() tea.Msg { return tea.WindowSizeMsg{Width: geo.Cols, Height: geo.Rows} }
}

// render draws the panel. Split from View so the layout stays a plain
// string-producing function: every test asserts on this text, and the
// viewport/renderer plumbing has no business in those assertions.
func (m Model) render() string {
	total := m.totalHeight()
	if total < 1 {
		return ""
	}
	if m.approvals {
		return strings.Join(m.appr.render(m.geo.Cols, total, apprChrome{arming: m.arming, armDiscarded: m.armDiscarded}), "\n")
	}
	cols := m.geo.Cols
	contentH := max(total-2, 0)

	feedW := m.feedWidth()

	header := styleHeader.Render(padCell(padCell(" agents", rosterPaneWidth)+"│"+padCell(" "+m.feedTitle(), feedW), cols))
	if m.banner != "" {
		header = styleSelected.Render(padCell(" "+m.banner, cols))
	}
	rosterLines := m.rosterLines(contentH)
	feedLines := splitPad(m.vp.View(), contentH)

	out := make([]string, 0, total)
	out = append(out, header)
	for i := 0; i < contentH; i++ {
		row := padCell(rosterLines[i], rosterPaneWidth) + "│" + padCell(feedLines[i], feedW)
		out = append(out, padCell(row, cols))
	}
	out = append(out, m.footerLine(cols))
	// One row of budget buys the header; the hint line is what a two-row panel
	// gives up last.
	if len(out) > total {
		out = out[:total]
	}
	return strings.Join(out, "\n")
}

// feedTitle names the feed under view and what is known about its agent.
func (m Model) feedTitle() string {
	if m.feedHarp == "" {
		return "feed: —"
	}
	r := m.selectedRow()
	meta := r.Agent
	if r.Engine != "" {
		if meta != "" {
			meta += "·"
		}
		meta += r.Engine
	}
	title := "feed: " + m.feedHarp
	if meta != "" {
		title += " (" + meta + ")"
	}
	if m.feedSource != "" {
		title += " · " + m.feedSource
	}
	if m.follow {
		title += " · ▼ follow"
	}
	return title
}

// footerLine is the panel's bottom row: the input line while it is open,
// otherwise the key hints plus the current note.
func (m Model) footerLine(cols int) string {
	if m.composeVerb != "" {
		// The input line replaces the hints while open: the verb, its explicit
		// target, the text so far, and its own key hints. Deliberately not
		// dimmed — it is the focused input.
		return padCell(" "+controlLabel(m.composeVerb)+" → "+m.composeHarp+": "+m.composeText+"_ · enter send · esc cancel", cols)
	}
	hints := " j/k move · enter feed · a approvals · i inject · ? ask · s summarize · p pause · r resume · " +
		"x expand · f follow · g/G ends · " + strings.ReplaceAll(m.prefixKey, "ctrl+", "^") + "/q back"
	// The note leads: it is the outcome of the key just pressed, and the key
	// list is what an ordinary width truncates.
	if note := m.hintNote(); note != "" {
		hints = " " + note + "  ─" + hints
	}
	return styleDim.Render(padCell(hints, cols))
}

func (m Model) selectedRow() RosterRow {
	if m.sel >= 0 && m.sel < len(m.rows) {
		return m.rows[m.sel]
	}
	return RosterRow{}
}

// rosterLines renders the agents pane windowed around the selection.
func (m Model) rosterLines(height int) []string {
	lines := make([]string, height)
	if len(m.rows) == 0 {
		return lines
	}
	offset := 0
	if len(m.rows) > height {
		offset = min(max(m.sel-height/2, 0), len(m.rows)-height)
	}
	for i := 0; i < height && offset+i < len(m.rows); i++ {
		r := m.rows[offset+i]
		label := r.Harp
		if r.Agent != "" {
			label += "·" + r.Agent
		}
		line := strings.Repeat("  ", min(r.Depth, 3)) + stateGlyph(r.State) + " " + label
		if offset+i == m.sel {
			line = styleSelected.Render(padCell(" "+line, rosterPaneWidth-1))
		} else {
			line = " " + line
		}
		lines[i] = line
	}
	return lines
}

// padCell pads/truncates s to exactly w terminal COLUMNS. Columns, not runes:
// the material framed here is engine transcript content, which carries
// double-width runes, and lipgloss-styled roster rows, whose SGR escapes are
// runes that occupy no column at all. Either one shears the pane divider off
// its column when counted as runes.
//
// Truncation goes through ansi.Truncate rather than a prefix scan of our own
// because a cut has to respect two things a scan does not: an escape sequence
// is indivisible (cutting inside one puts an unterminated CSI on the wire, and
// the terminal then eats every byte after it, including the pane divider),
// and a grapheme cluster is indivisible (a ZWJ sequence is one two-column
// glyph, not one column per code point). ansi.Truncate also re-emits the reset
// for any style still open at the cut, so the pad that follows is unstyled.
func padCell(s string, w int) string {
	if w < 1 {
		return ""
	}
	if n := lipgloss.Width(s); n <= w {
		return s + strings.Repeat(" ", w-n)
	}
	t := ansi.Truncate(s, w, "…")
	return t + strings.Repeat(" ", w-lipgloss.Width(t))
}

func splitPad(s string, h int) []string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}
