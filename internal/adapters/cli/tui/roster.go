package tui

import (
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The three labels an index session can carry when the coordinator has no
// state for it. They are the session lock's three verdicts, one each: there
// is no fourth, and none is a guess. The same words `ctxloom session list`
// prints in its STATE column (the frontend port's session vocabulary), and
// the ended word is the coordinator's own.
const (
	StateLive    = frontend.SessionLive
	StateEnded   = coord.StateEnded
	StateUnknown = frontend.SessionUnknown
)

// BuildRoster merges the session index (every session of this project, each
// with its lock's verdict) with the coordinator's runs (children with
// lineage and delivery state) into the tree's display rows: the running
// session pinned on top, children lineage-indented directly under their
// parent, and each level ordered by Activity, most recent first (ties by
// harp). A harp present in both keeps
// the index row's engine and takes the held row's richer agent/state; a held
// child with no index entry (e.g. a containerized child whose transcript
// hasn't landed) still shows.
func BuildRoster(index []frontend.Session, held []coord.RunInfo, selfHarp string) []RosterRow {
	return placeLineage(rosterRows(index, held, selfHarp), selfHarp)
}

// heldRow is the roster row the coordinator's run r says.
func heldRow(r coord.RunInfo) RosterRow {
	row := RosterRow{
		State: r.Phase, Parent: r.ParentHarp, Hold: r.Hold, Cause: r.Cause, Detail: r.Detail, Unread: r.Unread,
		LastActivity: r.LastEventAt, ContextTokens: r.ContextTokens, ContextWindow: r.ContextWindow,
	}
	if r.Agent != nil {
		row.Harp, row.Agent = r.Agent.AgentID, r.Agent.Role
	}
	return row
}

// rosterRows is the merge, in display order and before lineage indenting.
func rosterRows(index []frontend.Session, held []coord.RunInfo, selfHarp string) []RosterRow {
	heldByHarp := make(map[string]RosterRow, len(held))
	var heldOrder []RosterRow
	for _, r := range held {
		row := heldRow(r)
		if row.Harp == "" {
			continue
		}
		heldByHarp[row.Harp] = row
		heldOrder = append(heldOrder, row)
	}

	var rows []RosterRow
	seen := make(map[string]bool)
	addIndexRow := func(e frontend.Session) {
		h, ok := heldByHarp[e.Harp]
		rows = append(rows, indexRow(e, h, ok))
		seen[e.Harp] = true
	}
	// The running session first, then the rest in index order.
	for _, e := range index {
		if e.Harp == selfHarp {
			addIndexRow(e)
		}
	}
	for _, e := range index {
		if e.Harp != selfHarp {
			addIndexRow(e)
		}
	}
	for _, h := range heldOrder {
		if !seen[h.Harp] {
			rows = append(rows, h)
			seen[h.Harp] = true
		}
	}
	return rows
}

// indexRow renders one index entry, enriched by the coordinator's held row for
// the same harp when there is one.
func indexRow(e frontend.Session, held RosterRow, isHeld bool) RosterRow {
	row := RosterRow{Harp: e.Harp, Engine: e.Engine, SessionActivity: e.LastActivity}
	if !isHeld {
		row.State = e.State
		return row
	}
	// The held row is richer, not authoritative: an absent field is something
	// the coordinator has nothing to say about, so it must not erase what the
	// index already knows.
	if held.Agent != "" {
		row.Agent = held.Agent
	}
	if held.Parent != "" {
		row.Parent = held.Parent
	}
	// The coordinator's state wins when it has one: it runs that child and
	// knows. Without one it has nothing to say, and the lock's verdict stands.
	row.State = held.State
	row.Hold = held.Hold
	row.Cause, row.Detail = held.Cause, held.Detail
	row.Unread = held.Unread
	row.LastActivity = held.LastActivity
	row.ContextTokens, row.ContextWindow = held.ContextTokens, held.ContextWindow
	if row.State == "" {
		row.State = e.State
	}
	return row
}

// placeLineage moves children under their parent, depth-indented, each
// level ordered by activity (byActivity), with self — the running session —
// first among the roots. The child index is built in one
// pass, so the walk visits a node's actual children rather than rescanning
// every row per node.
func placeLineage(rows []RosterRow, self string) []RosterRow {
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.Harp] = true
	}
	// Roots are the rows with no parent, or a parent the roster doesn't know
	// (an orphan); everything else hangs off its parent.
	children := make(map[string][]RosterRow, len(rows))
	var roots []RosterRow
	for _, r := range rows {
		if r.Parent != "" && known[r.Parent] {
			children[r.Parent] = append(children[r.Parent], r)
		} else {
			roots = append(roots, r)
		}
	}
	if len(roots) > 0 && roots[0].Harp == self {
		// rosterRows put the running session first; it stays there.
		byActivity(roots[1:])
	} else {
		byActivity(roots)
	}
	for _, kids := range children {
		byActivity(kids)
	}

	placed := make(map[string]bool, len(rows))
	var out []RosterRow
	var place func(r RosterRow, depth int)
	place = func(r RosterRow, depth int) {
		if placed[r.Harp] {
			return
		}
		placed[r.Harp] = true
		r.Depth = depth
		out = append(out, r)
		for _, c := range children[r.Harp] {
			place(c, depth+1)
		}
	}
	for _, r := range roots {
		place(r, 0)
	}
	// Anything left is in a parent cycle that can't happen in practice — place
	// flat rather than lose it.
	for _, r := range rows {
		place(r, 0)
	}
	return out
}

// byActivity orders sibling rows most recently active first, ties by harp.
func byActivity(rows []RosterRow) {
	slices.SortFunc(rows, func(a, b RosterRow) int {
		if c := b.Activity().Compare(a.Activity()); c != 0 {
			return c
		}
		return strings.Compare(a.Harp, b.Harp)
	})
}
