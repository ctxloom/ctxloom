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
)

// The panel is the split view's left column: the session's name at the top,
// the agent tree, and at the bottom what the surround bar used to carry —
// approvals waiting, the last action's note, the key hints. There is no
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
	// foldOpen and foldClosed mark a row with children, expanded or not.
	foldOpen   = "▾"
	foldClosed = "▸"
)

// splitHints are the tree's keys, shown while it has focus.
const splitHints = "j/k move · enter fold · a approvals · i send · ? ask · s summarize · p pause · r resume · esc back"

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

// tokenLabel is how full r's context is: a percentage of its window, or the
// count alone when the window is unknown; "" when nothing has reported.
func tokenLabel(r RosterRow) string {
	switch {
	case r.ContextTokens == 0:
		return ""
	case r.ContextWindow > 0:
		return strconv.FormatUint(min(r.ContextTokens*100/r.ContextWindow, 999), 10) + "%"
	}
	return compactCount(r.ContextTokens)
}

// compactCount writes n in at most four columns: 950, 12k, 1.2M.
func compactCount(n uint64) string {
	switch {
	case n < 1000:
		return strconv.FormatUint(n, 10)
	case n < 1_000_000:
		return strconv.FormatUint(n/1000, 10) + "k"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
}

// visibleRows are the indices, into rows, of the tree's rows with every
// collapsed row's descendants left out.
func (s *Split) visibleRows() []int {
	var out []int
	for i := 0; i < len(s.rows); i++ {
		out = append(out, i)
		if s.collapsed[s.rows[i].Harp] {
			for d := s.rows[i].Depth; i+1 < len(s.rows) && s.rows[i+1].Depth > d; i++ {
			}
		}
	}
	return out
}

// visibleSel is the selection's position among the visible rows, or -1.
func (s *Split) visibleSel(visible []int) int {
	return slices.IndexFunc(visible, func(i int) bool { return s.rows[i].Harp == s.sel })
}

// windowStart is the first of h rows shown out of n, keeping row sel in the
// middle where it can.
func windowStart(sel, n, h int) int {
	return max(min(sel-h/2, n-h), 0)
}

// hasChildren reports whether rows[i] has rows under it: lineage order puts
// a row's children directly after it, one level deeper.
func (s *Split) hasChildren(i int) bool {
	return i+1 < len(s.rows) && s.rows[i+1].Depth > s.rows[i].Depth
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
	visible := s.visibleRows()
	sel := s.visibleSel(visible)
	start := windowStart(sel, len(visible), h)
	now := s.clock.Now()
	for y := 0; y < h && start+y < len(visible); y++ {
		lines[y] = s.treeLine(visible[start+y], w, start+y == sel, now)
	}
	return lines
}

// treeLine is rows[i]: indent, fold mark, glyph, harp·role and its
// annotations, the context gauge right-aligned.
func (s *Split) treeLine(i, w int, selected bool, now time.Time) string {
	r := s.rows[i]
	fold := ""
	if s.hasChildren(i) {
		fold = foldOpen
		if s.collapsed[r.Harp] {
			fold = foldClosed
		}
	}
	label := r.Harp
	if r.Agent != "" {
		label += "·" + r.Agent
	}
	if r.Cause != "" {
		label += " " + r.Cause
	}
	if r.Unread > 0 {
		label += " ✉" + strconv.Itoa(r.Unread)
	}
	body := strings.Repeat(" ", min(r.Depth, 3)) + fold + activityGlyph(r, now) + " " + label
	inner := w - 1
	if tok := tokenLabel(r); tok != "" {
		body = padCell(body, inner-len(tok)-1) + " " + tok
	}
	line := " " + padCell(body, inner)
	switch {
	case selected && s.focus == focusList:
		return styleSelected.Render(line)
	case selected:
		return styleHeader.Render(line)
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
	if note := s.noteLine(); note != "" {
		add(note, lipgloss.NewStyle())
	}
	if s.focus == focusList {
		add(splitHints, styleDim)
	} else {
		add(s.prefix.Caret()+" agents", styleDim)
	}
	return out
}

// pendingLine says how many approvals wait and how long the oldest has.
func (s *Split) pendingLine() string {
	line := pendingGlyph + " " + strconv.Itoa(s.pendingN) + " approval"
	if s.pendingN != 1 {
		line += "s"
	}
	if !s.pendingOldest.IsZero() {
		line += " · " + ageLabel(s.clock.Now().Sub(s.pendingOldest))
	}
	return line + " · a"
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
	visible := s.visibleRows()
	sel := s.visibleSel(visible)
	start := windowStart(sel, len(visible), h)
	now := s.clock.Now()
	for y := 0; y < h && start+y < len(visible); y++ {
		g := activityGlyph(s.rows[visible[start+y]], now) + " "
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
	case "enter":
		s.fold()
	case "esc", "q":
		s.focus = focusPane
	case "a":
		return s.openApprovals(false)
	case "i", "?", "s", "p", "r":
		return s.listControl(controlKeys[k])
	}
	return nil
}

// moveSel moves the selection d visible rows (from the top when it is on
// none) and re-targets the pane.
func (s *Split) moveSel(d int) tea.Cmd {
	visible := s.visibleRows()
	at := s.visibleSel(visible)
	if at < 0 {
		d = 0
	}
	return s.retarget(visible, min(max(at+d, 0), len(visible)-1))
}

// fold collapses or expands the selected row's subtree; on a row with
// none, Enter hands focus back to the pane.
func (s *Split) fold() {
	i := rowIndex(s.rows, s.sel)
	if i < 0 || !s.hasChildren(i) {
		s.focus = focusPane
		return
	}
	s.collapsed[s.sel] = !s.collapsed[s.sel]
}
