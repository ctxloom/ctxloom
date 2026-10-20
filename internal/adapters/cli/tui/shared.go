package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The split view's shared vocabulary: the panel's width, the messages its
// roster, feeds and controls deliver, the control verbs' keys and wording,
// and the cell padding every row is framed with.

// rosterPaneWidth is the agents pane's fixed column budget.
const rosterPaneWidth = 26

// Messages.
type rosterMsg struct{ rows []RosterRow }

type rosterErrMsg struct{ err error }

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
	ev   frontend.FeedEvent
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

// controlOutcome words one control round trip's outcome, and reports whether
// it failed.
func controlOutcome(msg controlResultMsg) (note string, failed bool) {
	harp := msg.req.Harp
	if msg.err != nil {
		return fmt.Sprintf("%s %s: %v", controlLabel(msg.req.Verb), harp, msg.err), true
	}
	switch msg.req.Verb {
	case coord.ControlVerbSteer:
		outcome := msg.res.Disposition
		if outcome == "" {
			outcome = msg.res.Delivery
		}
		return fmt.Sprintf("injected into %s: %s", harp, outcome), false
	case coord.ControlVerbQuestion, coord.ControlVerbSummarize:
		return fmt.Sprintf("asked %s (ask %s); its reply arrives as mail", harp, msg.res.AskID), false
	case coord.ControlVerbPause:
		return changedOr(msg.res.Changed, "paused "+harp, harp+" was already paused"), false
	case coord.ControlVerbResume:
		return changedOr(msg.res.Changed, "resumed "+harp, harp+" was not paused"), false
	}
	return "", false
}

func changedOr(changed bool, did, didNot string) string {
	if changed {
		return did
	}
	return didNot
}

// controlKeys maps each control key to the verb it drives.
var controlKeys = map[string]string{
	"i": coord.ControlVerbSteer,
	"?": coord.ControlVerbQuestion,
	"s": coord.ControlVerbSummarize,
	"p": coord.ControlVerbPause,
	"r": coord.ControlVerbResume,
}

// controlLabel is the verb's name on the split view. Steer keeps the split
// view's own word for it, "inject".
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

// controlPending is the hint while a request is in flight.
func controlPending(req coord.ControlRequest) string {
	switch req.Verb {
	case coord.ControlVerbSteer:
		return "injecting into " + req.Harp + "…"
	case coord.ControlVerbQuestion, coord.ControlVerbSummarize:
		return "asking " + req.Harp + "…"
	case coord.ControlVerbPause:
		return "pausing " + req.Harp + "…"
	}
	return "resuming " + req.Harp + "…"
}

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
)

// feedTitleOf names r's feed: its harp, agent and engine, hold, how it
// ended, unread mail, what feeds it, and whether the view follows its tail.
func feedTitleOf(r RosterRow, source string, follow bool) string {
	meta := r.Agent
	if r.Engine != "" {
		if meta != "" {
			meta += "·"
		}
		meta += r.Engine
	}
	title := "feed: " + r.Harp
	if meta != "" {
		title += " (" + meta + ")"
	}
	if hold := HoldLabel(r.Hold); hold != "" {
		title += " · " + hold
	}
	if ended := endedLabel(r); ended != "" {
		title += " · " + ended
	}
	if r.Unread > 0 {
		title += " · ✉ " + strconv.Itoa(r.Unread) + " unread"
	}
	if source != "" {
		title += " · " + source
	}
	if follow {
		title += " · ▼ follow"
	}
	return title
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
