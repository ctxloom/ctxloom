//go:build acceptance

// Package acceptance: the live half of the shared cell gate — the two questions
// that cannot be answered without a World and a reachable container runtime,
// composed on top of the pure decisions in capability_probe_gate.go.
//
// Nothing here decides anything on its own. It orders the gates by COST (the
// cheapest refusal first, so a cell never discovers at container-probe time
// something the liveAgents table already knew), records what the gate saw into
// the cell's evidence sidecar, and hands every refusal to probeCellSkip so the
// line reads the same whichever gate produced it.
package acceptance

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// probeCellGate runs the gates in cost order and returns the engine's liveAgent
// row together with its liveAgents key (the llm label a cell's config selects).
// A refusal comes back as godog.ErrSkip with the reason already printed; a
// MALFORMED cell (an unknown axis, an engine no liveAgents row covers) comes
// back as a hard error, because that is a bug in the feature file rather than a
// fact about this box.
//
// The cell is passed whole rather than as loose axis strings: P4's arms differ
// only by variant, and four positional strings is exactly the shape in which a
// caller eventually swaps two of them.
//
// It also populates w.docStepMaterialized with the availability report, so the
// evidence sidecar of any cell — green, red or skipped — records what the gate
// saw about the engine at the moment it decided.
func probeCellGate(c context.Context, w *World, family string, cell probeCellID) (liveAgent, string, error) {
	a, key, err := probeCellResolve(family, cell)
	if err != nil {
		return liveAgent{}, "", err
	}

	report, skip := probeCellDecide(probeEngine(key, a, realHomeDir, resolveOptIn()))
	w.docStepMaterialized = report
	if skip != "" {
		return liveAgent{}, "", probeCellSkip(family, cell, skip)
	}

	if cell.Workspace == "worktree" {
		if path, reason := probeWorktreeAuthAvailable(cell.Engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, cell,
				"worktree axis cannot authenticate this engine: "+reason)
		}
	}
	if cell.Runtime == "container" {
		if path, reason := probeContainerAuthAvailable(cell.Engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, cell,
				"container axis cannot authenticate this engine: "+reason)
		}
		if rt, _, msg := containercell.Select(c, "the "+family+" container cell"); !rt.Available {
			return liveAgent{}, "", probeCellSkip(family, cell,
				"no container runtime reachable here: "+msg)
		}
	}
	return a, key, nil
}
