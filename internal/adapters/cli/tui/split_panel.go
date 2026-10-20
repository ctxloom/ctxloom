package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/displaysafe"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The panel is the split view's left column: the session's name at the top,
// the agent tree, and at the bottom approvals waiting, the last action's note, the key hints. There is no
// bottom bar: on a terminal too narrow for the tree the panel becomes a
// column of glyphs, one per agent.

const (
	// launchingGlyph marks a run the coordinator has queued and not yet
	// started.
	launchingGlyph = "◌"
	// dozingGlyph marks an idle run that has been idle for idleDozeAfter.
	dozingGlyph = "○"
	// idleDozeAfter is how long an idle run waits before its glyph says it
	// has gone quiet.
	idleDozeAfter = 10 * time.Minute
	// pendingGlyph marks approvals waiting on the human.
	pendingGlyph = "⚑"
)

// noOwnControlNote is the note for a control key pressed on the session's
// own row, which is its terminal and takes none; it shows for
// noOwnControlFor.
const (
	noOwnControlNote = "i ? s p r act on a child: select one with j/k"
	noOwnControlFor  = 4 * time.Second
)

// splitHints are the tree's keys, shown while it has focus.
const splitHints = "j/k move · enter open · f/w filter · [ scroll · a approvals · i send · ? ask · s summarize · p/r pause/resume · esc back"

// activityGlyph is r's glyph in the split view's tree: launching, in a turn,
// idle (dozing once idle a while), held, ended, failed — and for a session
// the coordinator holds no run for, its lock's verdict.
func activityGlyph(r RosterRow, now time.Time) string {
	if r.Hold == nil {
		switch {
		case r.State == coord.StateQueued:
			return launchingGlyph
		case r.State == coord.StateIdle && !r.LastActivity.IsZero() && now.Sub(r.LastActivity) >= idleDozeAfter:
			return dozingGlyph
		}
	}
	return rowGlyph(r)
}

// selIndex is the selection's index among rows, or -1.
func (s *Split) selIndex() int { return rowIndex(s.rows, s.sel) }

// windowStart is the first of h rows shown out of n, keeping row sel in the
// middle where it can.
func windowStart(sel, n, h int) int {
	return max(min(sel-h/2, n-h), 0)
}

// rowIndex is harp's index among rows, or -1.
func rowIndex(rows []RosterRow, harp string) int {
	return slices.IndexFunc(rows, func(r RosterRow) bool { return r.Harp == harp })
}

// panelLines are the panel's h rows at width w.
func (s *Split) panelLines(w, h int) []string {
	if w == glyphColumnWidth {
		return s.glyphColumn(h)
	}
	head, foot := s.headerLines(w), s.footerLines(w)
	// The tree keeps at least one row; the header gives way first, then the
	// footer's oldest lines.
	for len(head)+len(foot)+1 > h && len(head) > 0 {
		head = head[:len(head)-1]
	}
	for len(head)+len(foot)+1 > h && len(foot) > 0 {
		foot = foot[1:]
	}
	tree := s.treeLines(w, h-len(head)-len(foot))
	out := make([]string, 0, h)
	out = append(out, head...)
	out = append(out, tree...)
	out = append(out, foot...)
	return out
}

// headerLines name the session: its harp, then what runs it.
func (s *Split) headerLines(w int) []string {
	name := s.header.Harp
	if name == "" {
		name = s.self
	}
	if s.header.Agent != "" {
		name += "·" + s.header.Agent
	}
	out := []string{styleHeader.Render(padCell(" "+name, w))}
	meta := s.header.Engine
	if s.header.Model != "" {
		if meta != "" {
			meta += "·"
		}
		meta += s.header.Model
	}
	if meta != "" {
		out = append(out, styleDim.Render(padCell(" "+meta, w)))
	}
	return out
}

// treeLines are the agent tree in h rows, windowed around the selection.
func (s *Split) treeLines(w, h int) []string {
	lines := make([]string, h)
	sel := s.selIndex()
	start := windowStart(sel, len(s.rows), h)
	now := s.clock.Now()
	for y := 0; y < h && start+y < len(s.rows); y++ {
		lines[y] = s.treeLine(start+y, w, start+y == sel, now)
	}
	return lines
}

// treeLine is rows[i]: indent, glyph, harp·role and its cause; right-aligned,
// the approvals and mail waiting, which a long name is cut short to keep. A
// row the filter keeps only for its place in the tree is dim.
func (s *Split) treeLine(i, w int, selected bool, now time.Time) string {
	r := s.rows[i]
	label := r.Harp
	if r.Agent != "" {
		label += "·" + r.Agent
	}
	if r.Cause != "" {
		label += " " + r.Cause
	}
	var right []string
	switch {
	case r.Pending > 1:
		right = append(right, pendingGlyph+strconv.Itoa(r.Pending))
	case r.Pending == 1:
		right = append(right, pendingGlyph)
	}
	if r.Unread > 0 {
		right = append(right, "✉ "+strconv.Itoa(r.Unread))
	}
	body := strings.Repeat(" ", min(r.Depth, 3)) + activityGlyph(r, now) + " " + label
	inner := w - 1
	if len(right) > 0 {
		tail := strings.Join(right, " ")
		body = padCell(body, inner-lipgloss.Width(tail)-1) + " " + tail
	}
	line := " " + padCell(body, inner)
	switch {
	case selected && s.focus == focusList:
		return styleSelected.Render(line)
	case selected:
		return styleHeader.Render(line)
	case r.Context:
		return styleDim.Render(line)
	}
	return line
}

// footerLines are the panel's bottom: a banner, approvals waiting, the
// note, and the key hints for whichever side has focus.
func (s *Split) footerLines(w int) []string {
	var out []string
	add := func(text string, style lipgloss.Style) {
		for _, l := range strings.Split(ansi.Wordwrap(text, w-1, ""), "\n") {
			out = append(out, style.Render(padCell(" "+l, w)))
		}
	}
	if s.banner != "" {
		add(s.banner, styleSelected)
	}
	if s.pendingN > 0 {
		add(s.pendingLine(), styleHeader)
	}
	if line := s.filterLine(); line != "" {
		add(line, styleDim)
	}
	switch note := s.noteLine(); {
	case s.errOneLine:
		// padCell cuts it, and says so.
		out = append(out, padCell(" "+note, w))
	case note != "":
		add(note, lipgloss.NewStyle())
	}
	switch {
	case s.focus == focusList:
		add(splitHints, styleDim)
	case s.scroll.on:
		add(splitScrollHints, styleDim)
	default:
		add(s.prefix.Caret()+" agents", styleDim)
	}
	return out
}

// pendingLine says how many approvals wait and how long the oldest has.
func (s *Split) pendingLine() string {
	line := pendingGlyph + " " + approvalsCount(s.pendingN)
	if !s.pendingOldest.IsZero() {
		line += " · " + ageLabel(s.clock.Now().Sub(s.pendingOldest))
	}
	return line + " · a"
}

// approvalsCount is "1 approval", "3 approvals".
func approvalsCount(n int) string {
	if n == 1 {
		return "1 approval"
	}
	return strconv.Itoa(n) + " approvals"
}

// ageLabel is a wait in the largest whole unit: 40s, 3m, 2h.
func ageLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(max(d, 0)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Hour)) + "h"
}

// noteLine is the one note the panel carries, in this order: the last
// action's failure, the roster's own failure, a timed note, the last
// action's outcome.
func (s *Split) noteLine() string {
	for _, n := range []string{s.errMsg, s.rosterErr, s.note, s.status} {
		if n != "" {
			return displaysafe.Text(n, true)
		}
	}
	return ""
}

// glyphColumn is the panel on a narrow terminal: one glyph per agent, the
// selection marked, and the approvals flag on the bottom row while any wait.
func (s *Split) glyphColumn(h int) []string {
	lines := make([]string, h)
	sel := s.selIndex()
	start := windowStart(sel, len(s.rows), h)
	now := s.clock.Now()
	for y := 0; y < h && start+y < len(s.rows); y++ {
		r := s.rows[start+y]
		g := activityGlyph(r, now) + " "
		if r.Pending > 0 {
			g = pendingGlyph + " "
		}
		if start+y == sel {
			g = styleSelected.Render(g)
		}
		lines[y] = g
	}
	if s.pendingN > 0 && h > 0 {
		lines[h-1] = styleHeader.Render(pendingGlyph + " ")
	}
	return lines
}

// listKey is a key while the tree has focus.
func (s *Split) listKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	switch k {
	case "j", "down":
		return s.moveSel(1)
	case "k", "up":
		return s.moveSel(-1)
	case "enter", "esc", "q":
		s.focus = focusPane
	case "f":
		s.statusFilter = s.statusFilter.Next()
		s.arrangeRows()
	case "w":
		s.listWindow = frontend.NextListWindow(s.listWindow)
		s.arrangeRows()
	case "[", "pgup":
		s.enterScroll(k == "pgup")
	case "a":
		return s.openApprovals(false)
	case "i", "?", "s", "p", "r":
		return s.listControl(controlKeys[k])
	}
	return nil
}

// moveSel moves the selection d rows (from the top when it is on none) and
// re-targets the pane.
func (s *Split) moveSel(d int) tea.Cmd {
	at := s.selIndex()
	if at < 0 {
		d = 0
	}
	return s.retarget(min(max(at+d, 0), len(s.rows)-1))
}
