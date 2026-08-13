//go:build acceptance

// Package acceptance: the capability ladder's SHARED CELL GATE — everything a
// live probe cell decides BEFORE it spends a paid turn, and the loud skip it
// prints when it decides not to.
//
// EXTRACTED FROM P0, NOT COPIED FROM IT. This is the exact gate stack
// engine_isolation_matrix.feature's Given step ran inline: is the engine
// installed and authenticated at all; can this specific AXIS authenticate it;
// and, for a container cell, is a runtime reachable here. Every probe in the
// ladder needs the same three answers in the same cost order, and a copy per
// probe would be three chances for a cell to spend a turn discovering something
// the gate already knew — or, worse, three skip messages that stop agreeing
// about what production can do.
//
// WHY THE AXIS RESOLVERS ARE REUSED RATHER THAN RE-DERIVED.
// probeWorktreeAuthAvailable and probeContainerAuthAvailable (isolation_probe.go)
// already encode production's own resolveEnvOrMountAuth / seedCredentials
// precedence per axis, including the engines whose axis simply cannot be
// authenticated today (kiro's, without KIRO_API_KEY). A probe that asked the
// question its own way would eventually disagree with what a run actually does,
// and the disagreement would surface as a mysterious red rather than as a gate.
//
// EVERY REFUSAL IS NAMED, AND NAMES SOMETHING PRODUCTION CANNOT DO. The standing
// rule for this suite is that a blank cell always has a reason attached and the
// reason is never "the harness declined to arrange it" — a matrix whose blanks
// are unexplained is indistinguishable from a matrix nobody ran.
package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// probeCellSkip prints the cell's own reason and skips. Never silent, and always
// naming the probe family, the engine and both axes.
//
// The FAMILY is part of the line because the ladder runs many probes over the
// same engine × axis grid: "SKIP [engine=codex runtime=host workspace=none]"
// read on its own cannot tell you whether the MCP round trip or the approach
// sweep declined to run.
func probeCellSkip(family, engine, runtime, workspace, reason string) error {
	fmt.Printf("SKIP %s cell [engine=%s runtime=%s workspace=%s]: %s\n",
		family, engine, runtime, workspace, reason)
	return godog.ErrSkip
}

// probeCellGate runs the three gates in cost order and returns the engine's
// liveAgent row together with its liveAgents key (the llm label a cell's config
// selects). A refusal comes back as godog.ErrSkip with the reason already
// printed; a MALFORMED cell (an unknown axis, an engine no liveAgents row
// covers) comes back as a hard error, because that is a bug in the feature file
// rather than a fact about this box — a row naming an unregistered engine would
// skip forever and read as coverage.
//
// It also populates w.docStepMaterialized with the availability report, so the
// evidence sidecar of any cell — green, red or skipped — records what the gate
// saw about the engine at the moment it decided.
func probeCellGate(c context.Context, w *World, family, engine, runtime, workspace string) (liveAgent, string, error) {
	switch runtime {
	case "host", "container":
	default:
		return liveAgent{}, "", fmt.Errorf("%s: unknown runtime axis %q (want host|container)", family, runtime)
	}
	switch workspace {
	case "none", "worktree":
	default:
		return liveAgent{}, "", fmt.Errorf("%s: unknown workspace axis %q (want none|worktree)", family, workspace)
	}

	key := backendTypeToLiveKey(engine)
	a, ok := liveAgents[key]
	if !ok {
		return liveAgent{}, "", fmt.Errorf("%s: %q (resolved key %q) is not registered in liveAgents (known: %v) — a row naming an unregistered engine would skip forever and look like coverage",
			family, engine, key, liveAgentOrder)
	}

	status := probeEngine(key, a, realHomeDir, resolveOptIn())
	w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
	if !status.available {
		return liveAgent{}, "", probeCellSkip(family, engine, runtime, workspace, status.reason)
	}

	if workspace == "worktree" {
		if path, reason := probeWorktreeAuthAvailable(engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, engine, runtime, workspace,
				"worktree axis cannot authenticate this engine: "+reason)
		}
	}
	if runtime == "container" {
		if path, reason := probeContainerAuthAvailable(engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, engine, runtime, workspace,
				"container axis cannot authenticate this engine: "+reason)
		}
		if rt, _, msg := containercell.Select(c, "the "+family+" container cell"); !rt.Available {
			return liveAgent{}, "", probeCellSkip(family, engine, runtime, workspace,
				"no container runtime reachable here: "+msg)
		}
	}
	return a, key, nil
}
