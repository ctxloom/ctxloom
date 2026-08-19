// Package acceptance: the capability ladder's SHARED CELL GATE — everything a
// live probe cell decides BEFORE it spends a paid turn, the loud skip it prints
// when it decides not to, and the credential posture every cell runs under.
//
// UNTAGGED, and that is the point of this file's shape. The gate's DECISIONS
// are facts about a feature file and about one availability answer; none of
// them needs a real engine, a container runtime or a godog scenario. Keeping
// them here means `just test` walks them — so inverting the availability test,
// or dropping the credential refusal, reds hermetically instead of waiting for
// a paid live run to behave strangely. The one part that genuinely needs a live
// World and a container runtime (probeCellGate) lives in the tagged half,
// capability_probe_gate_live.go, and is a thin composition of what is here.
//
// EXTRACTED FROM P0, NOT COPIED FROM IT — TWICE OVER. This was the gate stack
// engine_isolation_matrix.feature's Given step ran inline: is the engine
// installed and authenticated at all; can this specific AXIS authenticate it;
// and, for a container cell, is a runtime reachable here. P1 adopted the
// extraction immediately. P2, P3 and P4 were built in parallel worktrees and
// each re-typed the first two gates inline rather than take a five-way conflict
// on this file — a deliberate, recorded debt, paid off here now that the wave
// is mergeable. Three inline copies were three chances for a cell to spend a
// turn discovering something the gate already knew, or for three skip messages
// to stop agreeing about what production can do.
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
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// probeCellSkip prints the cell's own reason and skips. Never silent, and always
// naming the probe family and the full cell — engine, both axes, and the
// VARIANT when the cell has one.
//
// The FAMILY is part of the line because the ladder runs many probes over the
// same engine × axis grid: "SKIP [engine=codex runtime=host workspace=none]"
// read on its own cannot tell you whether the MCP round trip or the approach
// sweep declined to run. The VARIANT is part of it for the same reason one rung
// down: P4 runs a plan cell and its bypass control on identical axes, and a
// skip line that could not tell them apart would leave a reader unable to see
// whether the pair was half-run — which for a paired rung is the difference
// between a measurement and a provisional note.
func probeCellSkip(family string, cell probeCellID, reason string) error {
	// Value receiver, so this edit is local: the family already names the probe
	// on this line, and stamping it twice reads as two different identifiers.
	cell.Probe = ""
	fmt.Printf("SKIP %s cell %s: %s\n", family, cell, reason)
	return godog.ErrSkip
}

// probeCellResolve answers the questions that are facts about the FEATURE FILE
// rather than about this box: is the cell's axis vocabulary one the ladder
// knows, and does its engine resolve to a registered liveAgents row.
//
// Every failure here is a hard error, never a skip. A row naming an
// unregistered engine or a misspelt axis would skip forever and read as
// coverage — the exact silence this ladder exists to break — so it has to stop
// the suite rather than quietly decline.
func probeCellResolve(family string, cell probeCellID) (liveAgent, string, error) {
	// "container" stays valid alongside the two ownership values: most probes
	// have not yet migrated off the undifferentiated containerization axis,
	// and only P0 (engine_isolation_matrix.feature) declares
	// container-rootless/container-rootful cells today — see
	// capability_probe_gate_live.go's probeCellGate for where the two shapes
	// diverge into different runtime-availability checks.
	switch cell.Runtime {
	case "host", "container", "container-rootless", "container-rootful":
	default:
		return liveAgent{}, "", fmt.Errorf("%s: unknown runtime axis %q (want host|container|container-rootless|container-rootful)", family, cell.Runtime)
	}
	switch cell.Workspace {
	case "none", "worktree":
	default:
		return liveAgent{}, "", fmt.Errorf("%s: unknown workspace axis %q (want none|worktree)", family, cell.Workspace)
	}

	key := backendTypeToLiveKey(cell.Engine)
	a, ok := liveAgents[key]
	if !ok {
		return liveAgent{}, "", fmt.Errorf("%s: %q (resolved key %q) is not registered in liveAgents (known: %v) — a row naming an unregistered engine would skip forever and look like coverage",
			family, cell.Engine, key, liveAgentOrder)
	}
	return a, key, nil
}

// probeCellDecide folds the availability probe's answer into the gate's two
// outputs: the report every cell records — green, red or skipped — so its
// evidence sidecar says what the gate saw at the moment it decided, and a skip
// reason when this box cannot run the cell at all.
//
// Separate from probeCellResolve so this fold is exercised without an installed
// engine. It is the one line of the gate whose inversion would be invisible in
// a green run — an unavailable engine silently proceeding to buy a turn, or an
// available one skipping forever — and an untagged test asserts both directions.
func probeCellDecide(status engineStatus) (report, skip string) {
	report = formatLiveEngineReport([]engineStatus{status})
	if !status.available {
		return report, status.reason
	}
	return report, ""
}

// probeHostCredentialEnv rewrites a cell's command environment so that
// ctxloom's OWN per-axis credential machinery resolves against the REAL host
// home — because that is the mechanism under test, and starving it was making
// the matrix measure the harness instead of the product.
//
// WHAT WENT WRONG BEFORE, AND WHY THIS IS THE FIX. testenv isolates HOME to a
// temp dir, which is right for filesystem assertions and wrong here: EVERY
// production credential path resolves from hostHomeDir() — worktree.go's
// seedCredentials via credentialSeedSpecs, and the container mounts
// (claudeCredentialCopyMounts read-write, codexCredentialMounts /
// opencodeCredentialMounts read-only) all start there. Point HOME at an empty
// temp dir and every one of them finds nothing, so cells failed or had to be
// gated for reasons that exist nowhere outside this harness. Worse, the
// obvious workaround — the harness copying credentials into its fake home —
// would have made the cell MORE cautious than the product it verifies, and a
// cell that does not exercise production's credential mechanism proves nothing
// about it.
//
// So the credential mechanism is deliberately NOT isolated: HOME and the XDG
// roots point at the real ones, exactly as they do for a user typing this
// command. Everything a cell actually asserts on stays isolated — the project
// directory is still a fresh temp checkout carrying the fixture, and the
// assertion reads the run's own output or a fixture-owned file, never HOME. The
// cost is stated plainly: a cell writes session state under the real ~/.ctxloom
// and lets the engine refresh its own credential in place, which is precisely
// what a real run does and precisely what makes claude's rotating token safe
// here (the container credential mount shares the real store
// read-write, so a refresh lands in the live chain rather than dying in a copy).
//
// The fake entries are REMOVED before the real ones are appended, never merely
// appended after: a duplicate key in a child environment is resolved by the C
// library, and glibc's getenv returns the FIRST match, so appending alone
// would silently lose to the isolated value.
func probeHostCredentialEnv(env []string, realHome string) []string {
	shadowed := map[string]bool{
		"HOME": true, "USERPROFILE": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	}
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && shadowed[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"HOME="+realHome,
		"USERPROFILE="+realHome,
		"XDG_CONFIG_HOME="+filepath.Join(realHome, ".config"),
		"XDG_DATA_HOME="+filepath.Join(realHome, ".local", "share"),
	)
}

// probeCellCredentialEnv puts one cell's command under that posture, and
// REFUSES rather than degrading when the real home was never captured.
//
// The refusal is the whole reason this is a function and not an assignment.
// With realHomeDir empty, probeHostCredentialEnv would cheerfully export
// HOME="" and XDG_CONFIG_HOME="/.config": the run would start, the engine would
// find no credential where production looks for one, and the cell would report
// an engine failure that is entirely the harness's doing. Every probe in the
// ladder had typed this same guard inline; one copy per probe was one more
// chance for the next one to forget it and read the resulting red as a finding.
func probeCellCredentialEnv(family string, cmd *exec.Cmd) error {
	if realHomeDir == "" {
		return fmt.Errorf("%s: no real HOME was captured, so this cell cannot exercise production's own credential resolution", family)
	}
	cmd.Env = probeHostCredentialEnv(cmd.Env, realHomeDir)
	return nil
}
