package isolation

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// file-based oauth_creds.json under HOME is only a fallback for when no
// keyring is reachable at all (e.g. inside a container — see
// resolveAntigravityContainerAuth's doc for what is and is not confirmed
// about that fallback path).
//
// So a curated scratch HOME isolates CONFIG and SESSION STATE for an
// engine like this, but never AUTH — a fundamentally weaker boundary than
// credentialSeedSpecs' engines, which is why it is a SEPARATE registry
// (curatedHomeSpecs) rather than a credentialSeedSpec with an empty
// sourceFiles: conflating the two would let a partial lever read as full
// isolation.

// curatedHomeAllowlist is the FIXED, MINIMAL set of host dotfiles ctxloom
// makes reachable inside a curated scratch HOME. This is an allowlist, not
// a convenience default — extend it only when something CONCRETE breaks
// needing a new entry, never speculatively. Deliberately excluded:
// ~/.netrc, ~/.npmrc, ~/.gnupg — each carries plaintext tokens/keys of its
// own, and widening the allowlist to include them would widen exactly the
// blast radius this containment exists to limit.
var curatedHomeAllowlist = []string{".gitconfig", ".ssh"}

// curatedHomeSpec registers one engine whose ONLY host isolation lever is
// HOME itself. antigravity is the first, and — as of this change — the
// ONLY entry: opencode is a candidate under separate investigation and must
// NOT be wired here speculatively; a second engine opts in by adding one
// map entry, not by copying this file's mechanics.
type curatedHomeSpec struct {
	// engine names the backend for messages (e.g. "agy") — mirrors
	// credentialSeedSpec.engine.
	engine string
	// authIsolated reports whether pointing HOME at the curated scratch dir
	// ALSO relocates this engine's authentication. False for antigravity —
	// see the package-doc measurement above. A false spec fires
	// curatedHomeAuthFinding on every PrepareWorkspace; a (hypothetical)
	// true spec would not need to.
	authIsolated bool
	// containerAuthCaveat, when non-empty, is appended to the auth-not-
	// isolated finding's runtime:container nudge to say what actually
	// happens to auth inside the container for this engine — e.g. that the
	// keyring channel is severed at the namespace level and the engine fails
	// closed rather than authenticating. Not a hedge about missing plumbing:
	// say plainly when a container run simply cannot authenticate at all.
	containerAuthCaveat string
}

// curatedHomeSpecs is the opt-in registry Worktree.PrepareWorkspace consults
// (via curatedHomeSpecs[w.backend]), keyed by the REGISTERED backend name. A
// backend absent from BOTH this registry and credentialSeedSpecs has no
// known host isolation lever at all and keeps the pre-existing silent
// config-only no-op (Worktree.provisionConfigHome).
var curatedHomeSpecs = map[string]curatedHomeSpec{
	"antigravity": {
		engine:       "agy",
		authIsolated: false,
		containerAuthCaveat: "this is deliberate fail-closed behaviour, not a gap: a container's fresh mount/PID namespaces mean the keyring's UID-addressed socket (/run/user/<uid>/bus) does not exist inside the box, so a containerized agy run cannot authenticate at all instead of silently authenticating as the host user — see auth.go's resolveAntigravityContainerAuth",
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
// whose authIsolated is false.
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
