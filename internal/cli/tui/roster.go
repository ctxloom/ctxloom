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
	busByHarp := make(map[string]coord.RosterEntry, len(bus))
	for _, b := range bus {
		busByHarp[b.Harp] = b
	}

	var roots []RosterRow
	seen := make(map[string]bool)
	addIndexRow := func(e sessions.Entry) {
		state := "live"
		if e.EndedAt != nil {
			state = "ended"
		}
		row := RosterRow{Harp: e.HarpName, Engine: e.Backend, State: state}
		if b, ok := busByHarp[e.HarpName]; ok {
			// The held row is richer, not authoritative: an absent field is
			// something the coordinator has nothing to say about, so it must
			// not erase what the index already knows.
			if b.Agent != "" {
				row.Agent = b.Agent
			}
			if b.State != "" {
				row.State = b.State
			}
			if b.Parent != "" {
				row.Parent = b.Parent
			}
		}
		roots = append(roots, row)
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
			roots = append(roots, RosterRow{Harp: b.Harp, Agent: b.Agent, State: b.State, Parent: b.Parent})
			seen[b.Harp] = true
		}
	}

	// Lineage placement: children move under their parent, depth-indented.
	byHarp := make(map[string]int, len(roots))
	for i, r := range roots {
		byHarp[r.Harp] = i
	}
	placed := make(map[string]bool)
	var out []RosterRow
	var place func(r RosterRow, depth int)
	place = func(r RosterRow, depth int) {
		if placed[r.Harp] {
			return
		}
		placed[r.Harp] = true
		r.Depth = depth
		out = append(out, r)
		for _, c := range roots {
			if c.Parent == r.Harp {
				place(c, depth+1)
			}
		}
	}
	for _, r := range roots {
		// Roots: no parent, or a parent the roster doesn't know (orphan).
		if _, parentKnown := byHarp[r.Parent]; r.Parent == "" || !parentKnown {
			place(r, 0)
		}
	}
	// Anything left is in a parent cycle that can't happen in practice —
	// place flat rather than lose it.
	for _, r := range roots {
		place(r, 0)
	}
	return out
}
