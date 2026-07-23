package isolation

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// --- Curated scratch HOME: the mechanism for a "HOME-is-the-only-lever" engine ---
//
// credentialSeedSpecs (auth.go) covers engines that isolate via a SCOPED
// env var (CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME) — worktree.go points that
// one var at a per-agent subdir and the engine's global config/state/creds
// follow it, leaving the rest of the process env (including HOME itself)
// untouched. antigravity has no such var: agy reads $HOME directly for its
// config/session-state tree (a full .gemini/ materializes wherever HOME
// points) and has no ANTIGRAVITY_*/AGY_* var of its own — HOME is the
// mechanism or there is none.
//
// Measured (not inferred) on this host: pointing HOME at a scratch dir DOES
// relocate agy's config and conversation/session state — a fresh .gemini/
// tree materializes there, and chmod 000 on the fake HOME crashed agy,
// proving it reads the env var rather than falling back to the passwd
// entry. It does NOT relocate CREDENTIALS: agy authenticates through an
// OS-session-scoped D-Bus Secret Service keyring that is reachable
// regardless of HOME (verified with `env -i` and an empty fake HOME — agy's
// own log recorded "ChainedAuth: authenticated via keyring"). The
// file-based ~/.gemini/antigravity-cli/antigravity-oauth-token under HOME
// (NOT oauth_creds.json — an earlier, wrong guess corrected 2026-07-22) is
// only a fallback for when no keyring is reachable at all (e.g. inside a
// container — this is now the confirmed, seeded container-auth mechanism,
// see resolveAntigravityContainerAuth's doc).
//
// So a curated scratch HOME isolates CONFIG and SESSION STATE for an
// engine like this, but never AUTH — a fundamentally weaker boundary than
// credentialSeedSpecs' engines, which is why it is a SEPARATE registry
// (curatedHomeSpecs) rather than a credentialSeedSpec with an empty
// sourceFiles: conflating the two would let a partial lever read as full
// isolation.
//
// SECOND measurement, same day (2026-07-22), against agy 1.1.5: the
// workspace axis's OWN core promise — that an isolated cwd relocates an
// engine's file writes — also fails for antigravity. `agy -p` ignores the
// launch working directory ENTIRELY and always writes to a FIXED global
// scratch (~/.gemini/antigravity-cli/scratch/) regardless of which worktree
// it runs in, so two "isolated" agents share one global scratch tree no
// matter how many per-agent worktrees/HOMEs are set up. Combined with
// authIsolated==false, BOTH of a host worktree's two possible payoffs for
// this engine are absent — a curated HOME here only isolates config/session
// state (real, but not what a worktree request is FOR) — so PrepareWorkspace
// REFUSES a host worktree request for this engine outright
// (curatedHomeRefusal) instead of warning through a boundary that is not
// one. See curatedHomeSpec.workspaceViable and curatedHomeRefusal below.

// curatedHomeAllowlist is the FIXED, MINIMAL set of host dotfiles ctxloom
// makes reachable inside a curated scratch HOME. This is an allowlist, not
// a convenience default — extend it only when something CONCRETE breaks
// needing a new entry, never speculatively. Deliberately excluded:
// ~/.netrc, ~/.npmrc, ~/.gnupg — each carries plaintext tokens/keys of its
// own, and widening the allowlist to include them would widen exactly the
// blast radius this containment exists to limit.
var curatedHomeAllowlist = []string{".gitconfig", ".ssh"}

// curatedHomeSpec registers one engine whose ONLY host isolation lever is
// HOME itself. antigravity is the first, and — resolved by sunny-saga's
// investigation — the ONLY entry: opencode was the other candidate this
// registry's doc used to flag as "under separate investigation," but it
// turned out to have a FULL scoped-var lever instead (XDG_CONFIG_HOME /
// XDG_DATA_HOME, live-verified against opencode 1.18.1 — see
// credentialSeedSpecs["opencode"] in auth.go), so it is registered THERE,
// not here; wiring it into this registry too would be a mutual-exclusivity
// bug (PrepareWorkspace checks curatedHomeSpecs first and would silently
// shadow the scoped-var path with an unneeded blanket HOME override). A
// genuinely new HOME-only engine opts in by adding one map entry here, not
// by copying this file's mechanics.
type curatedHomeSpec struct {
	// engine names the backend for messages (e.g. "agy") — mirrors
	// credentialSeedSpec.engine.
	engine string
	// authIsolated reports whether pointing HOME at the curated scratch dir
	// ALSO relocates this engine's authentication. False for antigravity —
	// see the package-doc measurement above. A false spec fires a finding on
	// every host-worktree PrepareWorkspace — curatedHomeRefusal (fatal) when
	// workspaceViable is also false, else curatedHomeAuthFinding (warn); a
	// (hypothetical) true spec would not need either.
	authIsolated bool
	// workspaceViable reports whether a HOST {workspace: worktree} run is an
	// acceptable posture for this engine DESPITE authIsolated being false —
	// i.e. whether the worktree's OTHER possible payoff (relocating the
	// engine's file writes into the per-agent checkout) actually holds.
	// False for antigravity: MEASURED 2026-07-22 against agy 1.1.5 (see the
	// package doc above) — `agy -p` ignores the launch cwd entirely and
	// always writes to its own fixed global scratch, so the workspace axis
	// achieves NOTHING for antigravity's file writes on the host either.
	// With BOTH escapes unfixable on host (auth via the keyring, file writes
	// via the global scratch), PrepareWorkspace REFUSES a host worktree
	// request for this spec outright (curatedHomeRefusal) rather than warn
	// through a boundary that isolates neither of the two things a worktree
	// request is asking for. A spec with workspaceViable true (none exist
	// yet) would keep the pre-existing warn-only posture (curatedHomeAuthFinding)
	// even with authIsolated false — e.g. a hypothetical engine whose file
	// writes DO honour the launch cwd, so the worktree still isolates
	// something real even though auth doesn't.
	//
	// NEVER consulted for a Worktree wrapped inside a container base
	// (container_worktree.go's worktreeBase) — Worktree.containerWrapped
	// short-circuits the fatal branch there unconditionally, because a
	// container's own mount/PID namespace contains BOTH escapes regardless
	// of what this field says; the container path stays the pre-existing
	// warn-only posture (curatedHomeAuthFinding) it always had.
	workspaceViable bool
	// containerAuthCaveat, when non-empty, is appended to both the
	// auth-not-isolated warning AND the host workspace refusal's
	// runtime:container nudge to say what actually happens to auth inside
	// the container for this engine — e.g. that the keyring channel is
	// severed at the namespace level and the engine fails closed rather
	// than authenticating. Not a hedge about missing plumbing: say plainly
	// when a container run simply cannot authenticate at all.
	containerAuthCaveat string
}

// curatedHomeSpecs is the opt-in registry Worktree.PrepareWorkspace consults
// (via curatedHomeSpecs[w.backend]), keyed by the REGISTERED backend name. A
// backend absent from BOTH this registry and credentialSeedSpecs has no
// known host isolation lever at all and keeps the pre-existing silent
// config-only no-op (Worktree.provisionConfigHome).
var curatedHomeSpecs = map[string]curatedHomeSpec{
	"antigravity": {
		engine:          "agy",
		authIsolated:    false,
		workspaceViable: false,
		containerAuthCaveat: "this is deliberate fail-closed behaviour, not a gap: a container's fresh mount/PID namespaces mean the keyring's UID-addressed socket (/run/user/<uid>/bus) does not exist inside the box, so a containerized agy run authenticates ONLY via a seeded host file-based OAuth token (~/.gemini/antigravity-cli/antigravity-oauth-token) rather than silently authenticating as the host user through the keyring — with no such host token to seed, it fails closed instead — see auth.go's resolveAntigravityContainerAuth",
	},
}

// provisionCuratedHome creates home (0700 — same convention as every other
// scratch dir in this package: it will hold whatever config/session state
// the engine writes there) and symlinks each ALLOWLISTED host dotfile that
// exists into it. A host dotfile that does not exist (no ~/.gitconfig, no
// ~/.ssh) is skipped silently — never a dangling link. A missing gitconfig
// is a legitimate host state, not an error.
//
// SYMLINK, never copy. This is the whole point: home lives under the
// shared OS temp/session scratch root at 0700 because it already holds this
// engine's config/session state (i.e. it's already a credential-adjacent
// location); COPYING ~/.ssh's private key material there would duplicate a
// secret into that parent — and if cleanup ever fails to run (a crash, a
// killed process), the copy would outlive the run entirely. A symlink gives
// the engine identical reachability (both git and ssh follow symlinks
// transparently) with ZERO duplication: the key stays under ~/.ssh at its
// own permissions the whole time, and removing the curated home later only
// unlinks the pointer — os.RemoveAll never follows a symlink to recurse
// into (and so never deletes) the real target.
//
// Best-effort on the per-file symlink: one failed link loses that one
// convenience (git/ssh operations inside the curated HOME degrade to
// whatever their own defaults are), not the whole workspace.
func provisionCuratedHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create curated HOME: %w", err)
	}
	hostHome, err := hostHomeDir()
	if err != nil || hostHome == "" {
		return nil // no resolvable host HOME — curated dir stays usable, just empty
	}
	for _, name := range curatedHomeAllowlist {
		src := filepath.Join(hostHome, name)
		if _, statErr := os.Lstat(src); statErr != nil {
			continue // absent on the host — skip; never create a dangling link
		}
		_ = os.Symlink(src, filepath.Join(home, name))
	}
	return nil
}

// curatedHomeAuthFinding emits the LOUD, non-fatal warning that config and
// session-state isolation succeeded but authentication did not, for a spec
// whose authIsolated is false. Two callers reach it: a workspaceViable==true
// spec's host worktree (none exist yet — see the field's doc), and EVERY
// container-wrapped worktree (Worktree.containerWrapped), REGARDLESS of
// workspaceViable — a container's own mount/PID namespace already contains
// both of antigravity's escapes, so the container path keeps this exact
// pre-existing warn-only posture unconditionally; only the STANDALONE host
// path for a workspaceViable==false spec escalates, via curatedHomeRefusal
// below.
//
// Deliberately routed through clidiag.Warn, NEVER strictness.Fail/Record:
// this finding must not enter the same pass/fail signal a genuinely dropped
// isolation boundary uses (ClassIsolation — the choke owner aborts strict
// runs on that unless --degraded). A curated HOME that isolates config but
// not auth is a KNOWN, ACCEPTED partial boundary the run proceeds through
// by design; folding it into ClassIsolation would either block a run the
// user explicitly chose to accept, or — worse — read as "isolated" once
// --degraded is in play elsewhere in the same run. Config isolation and
// auth isolation are different things, and only one is happening here; the
// project has already been burned once by a claim this soft (kiro's global
// credential store under a "KIRO_HOME-isolated" per-agent home) — this
// finding exists so the same mistake is never possible to make silently
// again.
func curatedHomeAuthFinding(agentID string, spec curatedHomeSpec) {
	msg := fmt.Sprintf(
		"worktree isolation for agent %q: %s's CONFIGURATION and SESSION STATE are isolated in a curated HOME, but AUTHENTICATION IS NOT — %s authenticates through the OS session keyring (D-Bus Secret Service, addressed by UID via /run/user/<uid>/bus, not by $HOME), so this agent runs as the SAME account as every other %s agent on this host; switch this agent to runtime:container, which severs that socket at the namespace level for real auth isolation",
		agentID, spec.engine, spec.engine, spec.engine)
	if spec.containerAuthCaveat != "" {
		msg += " (" + spec.containerAuthCaveat + ")"
	}
	clidiag.Warn("ctxloom", "%s", msg)
}

// curatedHomeWorkspaceFixIt is the fix-it hint attached to curatedHomeRefusal's
// fatal finding: unlike credentialSeedFixIt (worktree.go), there is no
// host-side authenticate/env-var escape hatch here, because the problem is
// not creds alone — it is two independent, unfixable-on-host escapes
// (auth AND file writes). The only working alternative is runtime:container;
// --degraded is the same universal downgrade every other ClassIsolation
// finding in this package offers.
const curatedHomeWorkspaceFixIt = "switch this agent to runtime:container (its mount/PID namespace actually contains both escapes), or pass --degraded (env CTXLOOM_DEGRADED=1) to run this member with config/session isolation only, accepting shared auth AND shared global file writes"

// curatedHomeRefusal records the FATAL ClassIsolation finding that replaces
// curatedHomeAuthFinding's plain warning for a workspaceViable==false spec on
// the STANDALONE host worktree path — mirroring kiro's own fatal-unless-
// degraded posture (worktree.go's gateHomeVars/seedCredentials, the SAME
// strictness.Fail(ClassIsolation, ...) call the choke owner already aborts a
// strict run on, downgradable via --degraded exactly the same way).
//
// A curated scratch HOME for a spec like this isolates NEITHER of a host
// worktree's two possible payoffs: not AUTH (the OS session keyring reaches
// past $HOME) and not FILE WRITES (the engine ignores the launch cwd
// entirely and always writes to its own fixed global scratch, regardless of
// which worktree/HOME it was launched under — measured 2026-07-22, see the
// package doc). So the workspace axis achieves nothing worth the name for
// this engine on host, and PrepareWorkspace says so instead of warning
// through it. NEVER called for a container-wrapped Worktree
// (Worktree.containerWrapped short-circuits this branch in
// provisionCuratedHomeFor) — the container's own mount/PID namespace
// contains both escapes there, proven separately for antigravity; that path
// keeps curatedHomeAuthFinding's pre-existing warn-only posture unchanged.
func curatedHomeRefusal(agentID string, spec curatedHomeSpec) {
	msg := fmt.Sprintf(
		"worktree isolation for agent %q: %s cannot be isolated by a host worktree — "+
			"AUTHENTICATION escapes it (%s authenticates through the OS session keyring, D-Bus Secret Service, addressed by UID via /run/user/<uid>/bus, not by $HOME) "+
			"AND FILE WRITES escape it (%s ignores the launch working directory entirely and always writes to its own fixed global scratch, ~/.gemini/antigravity-cli/scratch/, regardless of which worktree/HOME it runs under) — "+
			"every %s agent on this host would share the SAME account AND the SAME scratch tree no matter how many isolated worktrees/HOMEs are set up; use runtime:container instead, which contains both escapes inside the container's own mount/PID namespace",
		agentID, spec.engine, spec.engine, spec.engine, spec.engine)
	if spec.containerAuthCaveat != "" {
		msg += " (" + spec.containerAuthCaveat + ")"
	}
	strictness.Fail(strictness.ClassIsolation, curatedHomeWorkspaceFixIt, "%s", msg)
}
