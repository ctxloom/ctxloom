package tui

import (
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// BuildRoster merges the session index (every session of this project) with
// the coordinator's live roster (children with lineage + delivery state,
// D2: coord.RosterEntry — was agentbus.RosterEntry before the bus package
// retired) into the overlay's display rows: index order preserved (most
// recent first, the running session pinned on top), children
// lineage-indented directly under their parent. A harp present in both keeps
// the index row's engine and takes the held row's richer agent/state; a
// held child with no index entry (e.g. a containerized child whose
// transcript hasn't landed) still shows.
func BuildRoster(index []sessions.Entry, bus []coord.RosterEntry, selfHarp string) []RosterRow {
	return placeLineage(rosterRows(index, bus, selfHarp))
}

// rosterRows is the merge, in display order and before lineage indenting.
func rosterRows(index []sessions.Entry, bus []coord.RosterEntry, selfHarp string) []RosterRow {
	busByHarp := make(map[string]coord.RosterEntry, len(bus))
	for _, b := range bus {
		busByHarp[b.Harp] = b
	}

	var rows []RosterRow
	seen := make(map[string]bool)
	addIndexRow := func(e sessions.Entry) {
		held, ok := busByHarp[e.HarpName]
		rows = append(rows, indexRow(e, held, ok))
		seen[e.HarpName] = true
	}
	// The running session first, then the rest in index order.
	for _, e := range index {
		if e.HarpName == selfHarp {
			addIndexRow(e)
		}
	}
	for _, e := range index {
		if e.HarpName != selfHarp {
			addIndexRow(e)
		}
	}
	for _, b := range bus {
		if !seen[b.Harp] {
			rows = append(rows, RosterRow{Harp: b.Harp, Agent: b.Agent, State: b.State, Parent: b.Parent})
			seen[b.Harp] = true
		}
	}
	return rows
}

// indexRow renders one index entry, enriched by the coordinator's held row for
// the same harp when there is one.
func indexRow(e sessions.Entry, held coord.RosterEntry, isHeld bool) RosterRow {
	state := "live"
	if e.EndedAt != nil {
		state = "ended"
	}
	row := RosterRow{Harp: e.HarpName, Engine: e.Backend, State: state}
	if !isHeld {
		return row
	}
	// The held row is richer, not authoritative: an absent field is something
	// the coordinator has nothing to say about, so it must not erase what the
	// index already knows.
	if held.Agent != "" {
		row.Agent = held.Agent
	}
	if held.State != "" {
		row.State = held.State
	}
	if held.Parent != "" {
		row.Parent = held.Parent
	}
	return row
}

// placeLineage moves children under their parent, depth-indented, preserving
// the order rows arrived in among siblings. The child index is built in one
// pass, so the walk visits a node's actual children rather than rescanning
// every row per node.
func placeLineage(rows []RosterRow) []RosterRow {
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
