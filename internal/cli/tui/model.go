package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	prefixKey string    // tea key name of the prefix (e.g. "ctrl+]")
	copyTo    io.Writer // OSC 52 sink (the tty)

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

// NewModel builds the overlay model. prefixByte is the interceptor's key;
// copyTo receives OSC 52 sequences (the tty in production).
func NewModel(ctx context.Context, src Sources, geo termui.OverlayGeometry, prefixByte byte, copyTo io.Writer) Model {
	m := Model{
		src:       src,
		ctx:       ctx,
		geo:       geo,
		prefixKey: teaKeyName(prefixByte),
		copyTo:    copyTo,
		firstKey:  true,
		follow:    true,
		expanded:  map[int]bool{},
		status:    statusLoading,
	}
	m.vp = viewport.New(m.feedWidth(), m.contentHeight())
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

// openFeedCmd cancels the current watch and opens the newly selected harp's.
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)

	case rosterMsg:
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
		if len(m.rows) > 0 {
			if m.status == statusLoading || m.status == statusNoSessions {
				m.status = ""
			}
			if !hadRows {
				return m, m.openFeed(m.rows[m.sel].Harp)
			}
		} else {
			m.status = statusNoSessions
		}
		return m, nil

	case rosterErrMsg:
		m.rosterErr = fmt.Sprintf("roster: %v", msg.err)
		return m, nil

	case rosterTickMsg:
		return m, tea.Batch(m.fetchRosterCmd(), rosterTick())

	case feedOpenedMsg:
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

	case feedErrMsg:
		if msg.harp == m.feedHarp {
			m.errMsg = fmt.Sprintf("feed %s: %v", msg.harp, msg.err)
		}
		return m, nil

	case feedEventMsg:
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

	case feedClosedMsg:
		if msg.harp == m.feedHarp {
			if msg.err != nil {
				m.errMsg = fmt.Sprintf("feed ended: %v", msg.err)
			} else {
				m.status = "feed ended (agent exited)"
			}
			m.feed = nil
		}
		return m, nil

	case injectResultMsg:
		if msg.err != nil {
			m.reportErr(fmt.Sprintf("inject %s: %v", msg.harp, msg.err))
		} else {
			m.reportOK(fmt.Sprintf("injected into %s: %s", msg.harp, msg.mode))
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.injecting {
		return m.updateInjectKey(msg)
	}
	key := msg.String()
	if m.firstKey {
		m.firstKey = false
		switch key {
		case "f": // prefix-then-f: full screen
			m.full = true
			m.resize()
			return m, tea.EnterAltScreen
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
	case "s":
		return m.export("txt")
	case "S":
		return m.export("ndjson")
	case "y":
		return m.copySelection()
	case "i":
		return m.openInject()
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
func (m Model) updateInjectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyRunes:
		m.injectText += string(msg.Runes)
		return m, nil
	case tea.KeySpace: // space is its own key type, not a rune
		m.injectText += " "
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
			return m, m.openFeed(m.rows[m.sel].Harp)
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
			return m, m.openFeed(m.rows[m.sel].Harp)
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

func (m Model) export(kind string) (tea.Model, tea.Cmd) {
	if m.feedHarp == "" || len(m.items) == 0 {
		m.reportOK("nothing to export yet")
		return m, nil
	}
	if m.src.ExportDir == nil {
		m.reportErr("export unavailable (no session dir)")
		return m, nil
	}
	dir, err := m.src.ExportDir(m.feedHarp)
	if err != nil {
		m.reportErr(fmt.Sprintf("export: %v", err))
		return m, nil
	}
	path, err := exportTranscript(dir, m.feedHarp, kind, m.items, m.src.now())
	if err != nil {
		m.reportErr(fmt.Sprintf("export: %v", err))
		return m, nil
	}
	m.reportOK("saved " + path)
	return m, nil
}

func (m Model) copySelection() (tea.Model, tea.Cmd) {
	if len(m.items) == 0 {
		m.reportOK("nothing to copy yet")
		return m, nil
	}
	text := copyText(m.items, m.cursor, m.focus == focusFeed)
	// osc52Copy("") is the OSC 52 CLEAR-selection form: emitting it would
	// wipe whatever the user already had on the clipboard and then report
	// success. The feed has items, but they rendered to nothing (a feed of
	// pure viewer chrome), so there is nothing to copy — say so.
	if text == "" {
		m.reportOK("nothing to copy: the selection holds no transcript content")
		return m, nil
	}

	var problems []string
	copied := false
	if m.copyTo != nil {
		if _, err := m.copyTo.Write(osc52Copy(text)); err != nil {
			problems = append(problems, fmt.Sprintf("clipboard write: %v", err))
		} else {
			copied = true
		}
	}
	// OSC 52 is fire-and-forget — a refusing terminal ignores it silently —
	// so always pair it with a file fallback and say so. That makes the
	// fallback the only observable delivery: when IT fails the user has no
	// signal at all, so its failure is reported rather than dropped.
	fallback := ""
	if m.src.ExportDir != nil {
		dir, err := m.src.ExportDir(m.feedHarp)
		if err != nil {
			problems = append(problems, fmt.Sprintf("fallback file: %v", err))
		} else if path, err := exportTranscript(dir, m.feedHarp, "txt", m.selectedItems(), m.src.now()); err != nil {
			problems = append(problems, fmt.Sprintf("fallback file: %v", err))
		} else {
			fallback = "; saved " + path + " in case the terminal ignored it"
		}
	}

	switch {
	case copied:
		m.status = "copied (OSC 52)" + fallback
	case fallback != "":
		m.status = "clipboard unavailable" + fallback
	default:
		m.status = ""
	}
	if len(problems) > 0 {
		m.errMsg = "copy: " + strings.Join(problems, "; ")
	} else {
		m.errMsg = ""
	}
	return m, nil
}

func (m Model) selectedItems() []feedItem {
	if m.focus == focusFeed && m.cursor >= 0 && m.cursor < len(m.items) {
		return m.items[m.cursor : m.cursor+1]
	}
	return m.items
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
		if top < m.vp.YOffset {
			m.vp.SetYOffset(top)
		} else if top >= m.vp.YOffset+m.vp.Height {
			m.vp.SetYOffset(top - m.vp.Height + 1)
		}
	}
}

func (m *Model) resize() {
	m.vp.Width = m.feedWidth()
	m.vp.Height = m.contentHeight()
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
func (m Model) View() string {
	total := m.totalHeight()
	if total < 1 {
		return ""
	}
	cols := m.geo.Cols
	contentH := max(total-2, 0)
	feedW := m.feedWidth()

	title := "feed: —"
	if m.feedHarp != "" {
		r := m.selectedRow()
		meta := r.Agent
		if r.Engine != "" {
			if meta != "" {
				meta += "·"
			}
			meta += r.Engine
		}
		title = "feed: " + m.feedHarp
		if meta != "" {
			title += " (" + meta + ")"
		}
		if m.feedSource != "" {
			title += " · " + m.feedSource
		}
		if m.follow {
			title += " · ▼ follow"
		}
	}
	header := padCell(" agents", rosterPaneWidth) + "│" + padCell(" "+title, feedW)

	rosterLines := m.rosterLines(contentH)
	feedLines := splitPad(m.vp.View(), contentH)

	out := make([]string, 0, total)
	out = append(out, styleHeader.Render(padCell(header, cols)))
	for i := 0; i < contentH; i++ {
		row := padCell(rosterLines[i], rosterPaneWidth) + "│" + padCell(feedLines[i], feedW)
		out = append(out, padCell(row, cols))
	}
	if m.injecting {
		// The inject line replaces the hints while open: explicit target, the
		// text so far, and its own key hints. Deliberately not dimmed — it is
		// the focused input.
		out = append(out, padCell(" inject → "+m.injectHarp+": "+m.injectText+"_ · enter send · esc cancel", cols))
	} else {
		hints := " j/k move · enter feed · i inject · x expand · f follow · g/G ends · s/S save · y copy · " +
			strings.ReplaceAll(m.prefixKey, "ctrl+", "^") + "/q back"
		if note := m.hintNote(); note != "" {
			hints += "  ─ " + note
		}
		out = append(out, styleDim.Render(padCell(hints, cols)))
	}
	// One row of budget buys the header; the hint line is what a two-row panel
	// gives up last.
	if len(out) > total {
		out = out[:total]
	}
	return strings.Join(out, "\n")
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

// padCell rune-pads/truncates s to exactly w cells (single-width content).
func padCell(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w < 1 {
			return ""
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

func splitPad(s string, h int) []string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}
