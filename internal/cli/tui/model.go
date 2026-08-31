package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/termui"
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

	injecting  bool   // the inject input line is open: keys type into it
	injectHarp string // the explicit target, latched when the line opens
	injectText string

	approving   bool                    // the approvals view is open: keys drive it, not the roster/feed
	approvals   []coord.PendingApproval // the last fetched pending list
	approvalSel int                     // selection into approvals

	approvalNoting     bool // the decline-note line is open: keys type into it
	approvalNoteText   string
	approvalTargetID   string // latched messageID for the in-flight answer
	approvalTargetHarp string // latched harp for the in-flight answer

	// The hint bar carries one line, chosen in this order: the last action's
	// failure, the roster's own failure, then status. Each slot is owned by
	// exactly one producer so that none of them outlives its subject:
	// rosterErr is retired by the next successful fetch, and an action
	// replaces both of its own slots at once.
	status    string // the last action's outcome, or a roster-owned note
	errMsg    string // the last action's failure
	rosterErr string // the roster fetch's failure, while it persists
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
type injectResultMsg struct {
	harp string
	mode string // coord.Delivery* on success (internal/agentcoord/coord)
	err  error
}
type approvalResultMsg struct {
	messageID string
	harp      string
	decision  agentcoordpb.ApprovalDecision_Decision
	err       error
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

// Init starts the roster fetch and its refresh tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchRosterCmd(), rosterTick())
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
		if m.approving {
			m = m.refreshApprovals()
		}
		return m, tea.Batch(m.fetchRosterCmd(), rosterTick())
	case feedOpenedMsg:
		return m.applyFeedOpened(msg)
	case feedErrMsg:
		return m.applyFeedErr(msg)
	case feedEventMsg:
		return m.applyFeedEvent(msg)
	case feedClosedMsg:
		return m.applyFeedClosed(msg)
	case injectResultMsg:
		return m.applyInjectResult(msg)
	case approvalResultMsg:
		return m.applyApprovalResult(msg)
	}
	return m, nil
}

func (m Model) applyFeedErr(msg feedErrMsg) (tea.Model, tea.Cmd) {
	if msg.harp == m.feedHarp {
		m.errMsg = fmt.Sprintf("feed %s: %v", msg.harp, msg.err)
	}
	return m, nil
}

func (m Model) applyInjectResult(msg injectResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.reportErr(fmt.Sprintf("inject %s: %v", msg.harp, msg.err))
	} else {
		m.reportOK(fmt.Sprintf("injected into %s: %s", msg.harp, msg.mode))
	}
	return m, nil
}

// applyApprovalResult lands the outcome of an answerApprovalCmd round trip.
// A failure — including the already_resolved race (another answerer won, or
// the rung timed out first) — surfaces as its own text via reportErr; it is
// never swallowed. A success refreshes the pending list so an answered
// approval disappears from the view immediately, rather than waiting for the
// next tick.
func (m Model) applyApprovalResult(msg approvalResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.reportErr(fmt.Sprintf("approval %s: %v", msg.messageID, msg.err))
		return m, nil
	}
	m.reportOK(fmt.Sprintf("approval %s: %s", msg.messageID, msg.decision.String()))
	if m.approving {
		m = m.refreshApprovals()
	}
	return m, nil
}

// refreshApprovals re-fetches the pending list (a plain in-process call, no
// I/O) and clamps the selection. An approval resolved elsewhere (another
// answerer, or a timeout) simply disappears from the next fetch. An empty
// list closes the view — symmetric with openApprovals refusing to open on an
// empty list — rather than leaving a blank panel open.
func (m Model) refreshApprovals() Model {
	if m.src.PendingApprovals == nil {
		return m
	}
	m.approvals = m.src.PendingApprovals()
	if len(m.approvals) == 0 {
		m.approving = false
		m.approvalNoting = false
		m.approvalNoteText = ""
		m.reportOK("no pending approvals")
		return m
	}
	if m.approvalSel >= len(m.approvals) {
		m.approvalSel = len(m.approvals) - 1
	}
	return m
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
	if m.injecting {
		return m.updateInjectKey(msg)
	}
	if m.approving {
		return m.updateApprovalKey(msg)
	}
	key := msg.String()
	if m.firstKey {
		m.firstKey = false
		switch key {
		case "f": // prefix-then-f: full screen
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
	case "i":
		return m.openInject()
	case "a":
		return m.openApprovals()
	}
	return m, nil
}

// openInject opens the inject input line targeting the viewed harp. The
// target is latched at open so a roster refresh can't silently retarget the
// text mid-composition.
func (m Model) openInject() (tea.Model, tea.Cmd) {
	if m.feedHarp == "" {
		m.reportOK("no agent selected to inject into")
		return m, nil
	}
	if m.src.Inject == nil {
		m.reportErr("inject unavailable (no agent bus for this session)")
		return m, nil
	}
	m.injecting = true
	m.injectHarp = m.feedHarp
	m.injectText = ""
	m.reportOK("")
	return m, nil
}

// updateInjectKey owns every key while the inject line is open: printable
// keys type into the line (including j/k — navigation is suspended), enter
// sends over the bus, esc cancels. The engine prefix and ctrl+c still back
// out of the overlay entirely.
func (m Model) updateInjectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// v2 reports printable input as Key.Text — one field covering both v1's
	// KeyRunes and its separate KeySpace. A non-printable key (enter, esc,
	// ctrl+c, the arrows) carries an empty Text and falls through to the
	// bindings below, which is the same precedence v1's type switch had.
	if msg.Text != "" {
		m.injectText += msg.Text
		return m, nil
	}
	switch msg.String() {
	case m.prefixKey, "ctrl+c":
		return m.quit()
	case "esc":
		m.injecting = false
		m.injectText = ""
		return m, nil
	case "enter":
		text := m.injectText
		if strings.TrimSpace(text) == "" {
			return m, nil
		}
		harp := m.injectHarp
		m.injecting = false
		m.injectText = ""
		m.status = "injecting into " + harp + "…"
		return m, m.injectCmd(harp, text)
	case "backspace":
		if r := []rune(m.injectText); len(r) > 0 {
			m.injectText = string(r[:len(r)-1])
		}
	}
	return m, nil
}

// injectCmd runs the bus round trip off the update loop; the outcome — the
// delivery mode, or the bus's typed error — renders inline on arrival.
func (m Model) injectCmd(harp, text string) tea.Cmd {
	inject := m.src.Inject
	return func() tea.Msg {
		mode, err := inject(harp, text)
		return injectResultMsg{harp: harp, mode: mode, err: err}
	}
}

// openApprovals opens the approvals view — a FULL body-region view (like the
// feed view, not a floating box: render() has no z-order primitive) listing
// every approval parked for this human, with the selected one's detail. It
// only opens on a non-empty list: an empty one hints instead, so the human
// is never staring at a panel with nothing in it.
func (m Model) openApprovals() (tea.Model, tea.Cmd) {
	if m.src.PendingApprovals == nil {
		m.reportErr("approvals unavailable (no coordinator for this session)")
		return m, nil
	}
	list := m.src.PendingApprovals()
	if len(list) == 0 {
		m.reportOK("no pending approvals")
		return m, nil
	}
	m.approving = true
	m.approvals = list
	m.approvalSel = 0
	m.reportOK("")
	return m, nil
}

// updateApprovalKey owns every key while the approvals view is open. The
// note line (declining) is nested state, checked first exactly like
// updateInjectKey's own printable-vs-binding precedence.
func (m Model) updateApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.approvalNoting {
		return m.updateApprovalNoteKey(msg)
	}
	switch msg.String() {
	case m.prefixKey, "ctrl+c":
		return m.quit()
	case "esc":
		m.approving = false
		m.approvals = nil
		m.approvalSel = 0
		return m, nil
	case "j", "down":
		if m.approvalSel < len(m.approvals)-1 {
			m.approvalSel++
		}
		return m, nil
	case "k", "up":
		if m.approvalSel > 0 {
			m.approvalSel--
		}
		return m, nil
	case "y":
		return m.answerSelected(agentcoordpb.ApprovalDecision_DECISION_ACCEPT, "")
	case "s":
		return m.answerSelected(agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION, "")
	case "n":
		return m.openApprovalNote()
	}
	return m, nil
}

// answerSelected latches the CURRENTLY selected approval's target
// (messageID+harp) at the keypress — exactly why openInject latches
// injectHarp at open — so a list refresh mid-flight (the async answer, or a
// background rosterTickMsg) cannot silently retarget an answer already in
// flight.
func (m Model) answerSelected(decision agentcoordpb.ApprovalDecision_Decision, note string) (tea.Model, tea.Cmd) {
	if m.approvalSel < 0 || m.approvalSel >= len(m.approvals) {
		return m, nil
	}
	sel := m.approvals[m.approvalSel]
	m.status = "answering " + sel.MessageID + "…"
	return m, m.answerApprovalCmd(sel.MessageID, sel.Harp, decision, note)
}

// openApprovalNote opens the decline-note line, latching the target from the
// current selection (the note itself is composed over several keystrokes,
// during which a background refresh must not retarget it).
func (m Model) openApprovalNote() (tea.Model, tea.Cmd) {
	if m.approvalSel < 0 || m.approvalSel >= len(m.approvals) {
		return m, nil
	}
	sel := m.approvals[m.approvalSel]
	m.approvalNoting = true
	m.approvalTargetID = sel.MessageID
	m.approvalTargetHarp = sel.Harp
	m.approvalNoteText = ""
	return m, nil
}

// updateApprovalNoteKey mirrors updateInjectKey's printable-accumulation
// idiom: enter sends DECISION_DECLINE with the note (empty is legal — the
// decline itself, not the note, is the decision), esc backs out to the LIST
// (not the whole overlay) without calling AnswerApproval at all.
func (m Model) updateApprovalNoteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.Text != "" {
		m.approvalNoteText += msg.Text
		return m, nil
	}
	switch msg.String() {
	case m.prefixKey, "ctrl+c":
		return m.quit()
	case "esc":
		m.approvalNoting = false
		m.approvalNoteText = ""
		return m, nil
	case "enter":
		note := m.approvalNoteText
		id, harp := m.approvalTargetID, m.approvalTargetHarp
		m.approvalNoting = false
		m.approvalNoteText = ""
		m.status = "answering " + id + "…"
		return m, m.answerApprovalCmd(id, harp, agentcoordpb.ApprovalDecision_DECISION_DECLINE, note)
	case "backspace":
		if r := []rune(m.approvalNoteText); len(r) > 0 {
			m.approvalNoteText = string(r[:len(r)-1])
		}
	}
	return m, nil
}

// answerApprovalCmd runs the coordinator round trip off the update loop; the
// outcome — resolved, or the bus's typed error (including the
// already_resolved race) — renders inline on arrival via applyApprovalResult.
func (m Model) answerApprovalCmd(messageID, harp string, decision agentcoordpb.ApprovalDecision_Decision, note string) tea.Cmd {
	answer := m.src.AnswerApproval
	if answer == nil {
		return func() tea.Msg {
			return approvalResultMsg{messageID: messageID, harp: harp, decision: decision,
				err: fmt.Errorf("approvals unavailable (no coordinator for this session)")}
		}
	}
	return func() tea.Msg {
		err := answer(messageID, harp, decision, note)
		return approvalResultMsg{messageID: messageID, harp: harp, decision: decision, err: err}
	}
}

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
	v.AltScreen = m.full
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
	cols := m.geo.Cols
	contentH := max(total-2, 0)

	if m.approving {
		return m.renderApprovals(total, cols, contentH)
	}

	feedW := m.feedWidth()

	header := padCell(" agents", rosterPaneWidth) + "│" + padCell(" "+m.feedTitle(), feedW)
	rosterLines := m.rosterLines(contentH)
	feedLines := splitPad(m.vp.View(), contentH)

	out := make([]string, 0, total)
	out = append(out, styleHeader.Render(padCell(header, cols)))
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

// renderApprovals draws the approvals view: a FULL body-region view (like
// the ordinary render(), sharing its header+content+footer budget), not a
// floating box over the roster/feed — this package has no z-order primitive
// and render()'s own "own exactly totalHeight lines" invariant (the overlay
// paints over a live engine session) applies here identically.
func (m Model) renderApprovals(total, cols, contentH int) string {
	header := padCell(fmt.Sprintf(" approvals (%d pending)", len(m.approvals)), cols)
	lines := m.approvalLines(contentH)

	out := make([]string, 0, total)
	out = append(out, styleHeader.Render(header))
	for i := 0; i < contentH; i++ {
		out = append(out, padCell(lines[i], cols))
	}
	out = append(out, m.approvalFooterLine(cols))
	if len(out) > total {
		out = out[:total]
	}
	return strings.Join(out, "\n")
}

// approvalLines renders the pending list (selection marked) followed by the
// SELECTED approval's detail: harp, kind, title, expires-in, and the
// pretty-printed payload — truncated to the panel's remaining height.
func (m Model) approvalLines(height int) []string {
	var lines []string
	for i, a := range m.approvals {
		marker := "  "
		if i == m.approvalSel {
			marker = "> "
		}
		lines = append(lines, marker+a.Harp+" · "+a.Title)
	}
	if sel, ok := m.selectedApproval(); ok {
		lines = append(lines, "")
		lines = append(lines, "harp: "+sel.Harp)
		lines = append(lines, "kind: "+sel.Kind.String())
		lines = append(lines, "title: "+sel.Title)
		lines = append(lines, "expires in "+formatExpiresIn(sel.Deadline, m.src.now()))
		lines = append(lines, "payload:")
		lines = append(lines, prettyPayloadLines(sel.Payload)...)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// selectedApproval is the approval approvalSel currently points at, if any —
// a bounds-checked read shared by rendering and the y/s/n key handlers.
func (m Model) selectedApproval() (coord.PendingApproval, bool) {
	if m.approvalSel < 0 || m.approvalSel >= len(m.approvals) {
		return coord.PendingApproval{}, false
	}
	return m.approvals[m.approvalSel], true
}

// formatExpiresIn renders a Deadline relative to now in whole minutes. A
// zero Deadline (a fake Sources in a test, or a coordinator that predates
// slice 3) renders "unknown" rather than a nonsense negative duration.
func formatExpiresIn(deadline, now time.Time) string {
	if deadline.IsZero() {
		return "unknown"
	}
	d := deadline.Sub(now)
	if d <= 0 {
		return "0m (expired)"
	}
	return fmt.Sprintf("%dm", int(d.Minutes())+1)
}

// prettyPayloadLines indents the approval's JSON payload (json.Indent) and
// splits it into render lines. An empty/malformed payload renders a plain
// placeholder rather than failing the whole view over one bad field.
func prettyPayloadLines(payload json.RawMessage) []string {
	if len(payload) == 0 {
		return []string{"  (no payload)"}
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, payload, "  ", "  "); err != nil {
		return []string{"  " + string(payload)}
	}
	return strings.Split("  "+buf.String(), "\n")
}

// approvalFooterLine is the approvals view's bottom row: the decline-note
// input while it is open, otherwise the key hints plus the current note —
// the same hintNote precedence footerLine uses.
func (m Model) approvalFooterLine(cols int) string {
	if m.approvalNoting {
		return padCell(" decline note: "+m.approvalNoteText+"_ · enter send · esc back", cols)
	}
	hints := " j/k select · y accept · s accept-for-session · n decline+note · esc back"
	if note := m.hintNote(); note != "" {
		hints += "  ─ " + note
	}
	return styleDim.Render(padCell(hints, cols))
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

// footerLine is the panel's bottom row: the inject input while it is open,
// otherwise the key hints plus the current note.
func (m Model) footerLine(cols int) string {
	if m.injecting {
		// The inject line replaces the hints while open: explicit target, the
		// text so far, and its own key hints. Deliberately not dimmed — it is
		// the focused input.
		return padCell(" inject → "+m.injectHarp+": "+m.injectText+"_ · enter send · esc cancel", cols)
	}
	hints := " j/k move · enter feed · i inject · x expand · f follow · g/G ends · s/S save · y copy · " +
		strings.ReplaceAll(m.prefixKey, "ctrl+", "^") + "/q back"
	if note := m.hintNote(); note != "" {
		hints += "  ─ " + note
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
