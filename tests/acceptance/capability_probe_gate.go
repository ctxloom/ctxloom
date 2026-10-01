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
// precedence per axis, including any engine whose axis simply cannot be
// authenticated today. A probe that asked the
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

	"github.com/ctxloom/ctxloom/internal/core/launch"
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
//
// This is the GHERKIN SEAM for the runtime axis: cell.Runtime is a string
// straight out of an Examples-table column (a Go type cannot cross the
// feature-file/subprocess boundary), and this is the ONE place it gets
// parsed — via the same launch.ParseRuntimeAxis every other boundary uses,
// never a local switch or string compare. A row naming a value that does not
// resolve — including the retired undifferentiated "container" (task
// unwatched-discharge split it into container-rootless/container-rootful;
// there is deliberately no "any container" value) — fails the step here,
// naming the bad value and the legal ones, rather than skipping forever or
// falling through to whatever cell.Runtime's zero-value behavior happens to
// be.
func probeCellResolve(family string, cell probeCellID) (liveAgent, string, error) {
	if _, err := launch.ParseRuntimeAxis(cell.Runtime); err != nil {
		return liveAgent{}, "", fmt.Errorf("%s: %w", family, err)
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
// home. It is the FALLBACK posture, taken only when no token was captured at
// launch (probeCellCredentialEnv decides): a subscription login lives in the
// real home and nowhere else, so this is the only way such a cell can
// authenticate.
//
// WHY NOT A COPY. testenv isolates HOME to a temp dir, and EVERY production
// credential path resolves from hostHomeDir() — worktree.go's seedCredentials
// via the engine's declared credential seed, and the container mounts
// (claudeCredentialCopyMounts read-write) all start there. The obvious
// workaround — the harness copying the login into its fake home — would make
// the cell MORE cautious than the product it verifies, and a rotating login
// refreshed inside a copy dies with the copy. The cost of the real home is
// stated plainly: a cell writes session state under the real ~/.ctxloom and
// lets the engine refresh its own credential in place, exactly as a real run
// does.
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

// probeCellCredentialEnv puts one ctxloom-driven cell's command under its
// credential posture and returns the HOME the run will use — callers that
// watch ctxloom's session tree must look under THAT home, not assume one.
//
//   - A token captured at launch (liveCredential): the command keeps testenv's
//     ISOLATED HOME (so claude's config dir, HOME/.claude, is isolated too) and
//     gains the token. ctxloom's own env-first credential resolution carries it
//     to the engine on every axis; nothing in the real home is read or written.
//   - No token: the real-home login (probeHostCredentialEnv), and a REFUSAL
//     rather than a degraded run when the real home was never captured. With
//     realHomeDir empty, probeHostCredentialEnv would cheerfully export
//     HOME="" and XDG_CONFIG_HOME="/.config": the run would start, find no
//     credential, and report an engine failure that is the harness's doing.
func probeCellCredentialEnv(family, engine string, cmd *exec.Cmd) (string, error) {
	if cred, ok := liveCredential(liveAgents[backendTypeToLiveKey(engine)]); ok {
		cmd.Env = append(envWithout(cmd.Env, cred.EnvVar), cred.EnvVar+"="+cred.Value)
		return envValue(cmd.Env, "HOME"), nil
	}
	if realHomeDir == "" {
		return "", fmt.Errorf("%s: no token was captured at launch and no real HOME was captured, so this cell has no credential to run under", family)
	}
	cmd.Env = probeHostCredentialEnv(cmd.Env, realHomeDir)
	return realHomeDir, nil
}

// envWithout drops every key= entry: glibc's getenv returns the FIRST match,
// so an appended value must not have a namesake ahead of it.
func envWithout(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envValue is the value getenv would return for key: the FIRST match.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}
