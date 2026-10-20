package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// A delegated child is headless: the split view shows it as its feed (the
// port's WatchFeed, live while the coordinator holds the child, else its
// transcript's tail) above an input line. What the human types there goes to
// the child through the port's Control as the human's send — a steer, which
// the child takes at its next turn — or, with alt+enter, a steer that
// interrupts the turn in flight. There is no child terminal to type into.

const (
	// feedPage is how far PgUp/PgDn scroll a child's feed.
	feedPage = 10
	// feedRetryEvery is how often a child with nothing captured yet has its
	// feed opened again: it starts once the child's first turn is recorded.
	feedRetryEvery = 2 * time.Second
	// feedWaitingNote is the pane while a child's feed has not started.
	feedWaitingNote = "no turns captured yet — the feed starts with the first"
)

// feedRetryMsg is a feed open falling due again for the child the pane
// showed when token was its.
type feedRetryMsg struct {
	harp  string
	token *feedToken
}

// feedToken is one showing of a child in the pane: each open mints a fresh
// one, so a retry scheduled for an earlier showing — even of the same harp —
// is told apart. Not zero-sized, so every token is a distinct pointer.
type feedToken struct{ _ byte }

// childView is the selected child's half of the pane.
type childView struct {
	harp   string
	source string
	feed   *Feed
	items  []feedItem
	// waiting: nothing is captured for the child yet, and its feed's open
	// is retried until something is.
	waiting bool
	token   *feedToken
	// scroll is how many lines above the tail the view sits; 0 follows it.
	scroll int
	// verb is the input line's control verb: a steer (the human's send)
	// unless ? or s on the tree latched a question or a summarize.
	verb string
	text string
}

// open switches the view to harp; the previous feed's watch is released.
func (c *childView) open(harp string) {
	c.close()
	c.harp = harp
	c.verb = coord.ControlVerbSteer
	c.token = &feedToken{}
}

// close releases the feed's watch (openFeed always gives it a Cancel) and
// forgets the child.
func (c *childView) close() {
	if c.feed != nil {
		c.feed.Cancel()
	}
	*c = childView{}
}

// inputCursor is the cursor's column within the pane: the end of the typed
// text.
func (s *Split) inputCursor() int {
	return lipgloss.Width(s.inputPrompt()) + lipgloss.Width(s.child.text)
}

// inputPrompt is the input line's lead: the verb and its target.
func (s *Split) inputPrompt() string {
	label := "send"
	if s.child.verb != coord.ControlVerbSteer {
		label = controlLabel(s.child.verb)
	}
	return " " + label + " → " + s.child.harp + ": "
}

// retarget points the right pane at rows[i]: the engine's terminal for the
// session's own row, the child's feed for any other. i is -1 only when there
// are no rows.
func (s *Split) retarget(i int) tea.Cmd {
	if i < 0 || s.rows[i].Harp == s.sel {
		return nil
	}
	s.sel = s.rows[i].Harp
	if s.showingTerminal() {
		s.child.close()
		return nil
	}
	s.child.open(s.sel)
	return s.openFeedCmd(s.sel)
}

func (s *Split) applyChildFeedMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case feedOpenedMsg:
		if msg.harp != s.child.harp {
			// Stale open (the selection moved on): release it.
			if msg.feed.Cancel != nil {
				msg.feed.Cancel()
			}
			return nil
		}
		s.child.feed = msg.feed
		s.child.source = msg.feed.Source
		s.child.waiting = false
		return s.waitFeedCmd()
	case feedErrMsg:
		if msg.harp != s.child.harp {
			return nil
		}
		if errors.Is(msg.err, frontend.ErrNothingCaptured) {
			s.child.waiting = true
			retry := feedRetryMsg{harp: msg.harp, token: s.child.token}
			s.clock.AfterFunc(feedRetryEvery, func() { s.post(retry) })
			return nil
		}
		s.reportFeedErr(fmt.Sprintf("feed %s: %v", msg.harp, msg.err))
	case feedRetryMsg:
		if msg.token != s.child.token {
			return nil
		}
		return s.openFeedCmd(msg.harp)
	case feedEventMsg:
		if msg.harp != s.child.harp || s.child.feed == nil {
			return nil
		}
		add := itemsFromFeedEvent(msg.ev)
		s.child.items = append(s.child.items, add...)
		return s.waitFeedCmd()
	case feedClosedMsg:
		if msg.harp != s.child.harp {
			return nil
		}
		if msg.err != nil {
			s.reportFeedErr(fmt.Sprintf("feed ended: %v", msg.err))
		} else {
			s.reportOK("feed ended (agent exited)")
		}
		s.child.feed = nil
	}
	return nil
}

// childKey is a key for the child's input line: text types, Enter sends,
// alt+Enter sends interrupting, Esc clears, the paging keys scroll the feed.
func (s *Split) childKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.Text != "" {
		s.child.text += msg.Text
		return nil
	}
	switch msg.String() {
	case "enter":
		return s.sendInput(false)
	case "alt+enter":
		return s.sendInput(true)
	case "backspace":
		if r := []rune(s.child.text); len(r) > 0 {
			s.child.text = string(r[:len(r)-1])
		}
	case "esc":
		s.child.text = ""
		s.child.verb = coord.ControlVerbSteer
	case "up":
		s.scrollFeed(1)
	case "down":
		s.child.scroll = max(s.child.scroll-1, 0)
	case "pgup":
		s.scrollFeed(feedPage)
	case "pgdown":
		s.child.scroll = max(s.child.scroll-feedPage, 0)
	case "end":
		s.child.scroll = 0
	}
	return nil
}

// scrollFeed moves the feed's view n lines up, no further than its top.
func (s *Split) scrollFeed(n int) {
	_, paneW := s.layout()
	s.child.scroll = min(s.child.scroll+n, s.maxScroll(len(s.feedLines(paneW)), s.feedHeight(s.h)))
}

// feedLines are the child's feed rendered at width w, with no cursor.
func (s *Split) feedLines(w int) []string {
	lines, _ := renderItems(s.child.items, w, nil, -1)
	return lines
}

// feedHeight is how many feed rows a pane of h rows has: all but its title
// and its input line.
func (s *Split) feedHeight(h int) int { return max(h-2, 0) }

// maxScroll is how far above the tail a feed of n lines in a body of h rows
// can sit: none while it fits.
func (s *Split) maxScroll(n, h int) int { return max(n-h, 0) }

// sendInput sends the input line to the child. An interrupt is a steer's
// alone; a latched question or summarize goes as itself.
func (s *Split) sendInput(interrupt bool) tea.Cmd {
	if strings.TrimSpace(s.child.text) == "" {
		return nil
	}
	req := coord.ControlRequest{Verb: s.child.verb, Harp: s.child.harp, Body: s.child.text}
	req.Interrupt = interrupt && req.Verb == coord.ControlVerbSteer
	s.child.text = ""
	s.child.verb = coord.ControlVerbSteer
	return s.sendControl(req)
}

// sendControl runs one control round trip off the update loop.
func (s *Split) sendControl(req coord.ControlRequest) tea.Cmd {
	if s.port == nil {
		s.reportErr(controlLabel(req.Verb) + " unavailable (no agent bus for this session)")
		return nil
	}
	s.reportOK(controlPending(req))
	control, ctx := s.port.Control, s.ctx
	return func() tea.Msg {
		res, err := control(ctx, req)
		return controlResultMsg{req: req, res: res, err: err}
	}
}

// applyControlResult lands one control round trip's outcome as the panel's
// note.
func (s *Split) applyControlResult(msg controlResultMsg) {
	note, failed := controlOutcome(msg)
	if failed {
		s.reportErr(note)
		return
	}
	s.reportOK(note)
}

func (s *Split) reportOK(note string)  { s.status, s.errMsg, s.errOneLine = note, "", false }
func (s *Split) reportErr(note string) { s.status, s.errMsg, s.errOneLine = "", note, false }

// reportFeedErr is a feed's failure as the note: one line, cut to the
// panel's width, since an error from below the port can run a paragraph.
func (s *Split) reportFeedErr(note string) {
	s.reportErr(note)
	s.errOneLine = true
}

// listControl acts on the tree's selected row: pause and resume at once; a
// verb that carries text latches the input line and hands it focus.
func (s *Split) listControl(verb string) tea.Cmd {
	if s.showingTerminal() {
		s.flash(noOwnControlNote, noOwnControlFor)
		return nil
	}
	if !controlTakesBody(verb) {
		return s.sendControl(coord.ControlRequest{Verb: verb, Harp: s.sel})
	}
	s.child.verb = verb
	s.focus = focusPane
	return nil
}

// childLines are the child's pane: the feed's title, the feed (following
// its tail unless scrolled), and the input line.
func (s *Split) childLines(w, h int) []string {
	out := make([]string, 0, h)
	out = append(out, styleHeader.Render(padCell(" "+s.childTitle(), w)))
	bodyH := s.feedHeight(h)
	lines := s.feedLines(w)
	if s.child.waiting {
		lines = []string{styleDim.Render(" " + feedWaitingNote)}
	}
	end := len(lines) - min(s.child.scroll, s.maxScroll(len(lines), bodyH))
	for _, l := range lines[max(end-bodyH, 0):end] {
		out = append(out, padCell(l, w))
	}
	for len(out) < h-1 {
		out = append(out, strings.Repeat(" ", w))
	}
	out = append(out, s.inputLine(w))
	return out[:h]
}

// childTitle names the feed under view and what is known about its agent.
func (s *Split) childTitle() string {
	return feedTitleOf(s.row(s.child.harp), s.child.source, s.child.scroll == 0)
}

// row is harp's roster row; a harp the roster does not hold is a row with
// only its name.
func (s *Split) row(harp string) RosterRow {
	for _, r := range s.rows {
		if r.Harp == harp {
			return r
		}
	}
	return RosterRow{Harp: harp}
}

// inputLine is the child's input line: the verb and target, the text so
// far, and its keys.
func (s *Split) inputLine(w int) string {
	line := s.inputPrompt() + s.child.text
	if s.child.text == "" {
		line += styleDim.Render("enter send · alt+enter interrupt · esc clear")
	}
	return padCell(line, w)
}
