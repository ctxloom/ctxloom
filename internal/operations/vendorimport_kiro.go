package operations

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	kiroimporter "github.com/ctxloom/ctxloom/internal/transcript/importer/kiro"
)

// locateKiroConversation resolves kiro's composite "<db-path>#<conversation-id>"
// locator (kiroimporter.Locator) for one indexed entry — the one engine
// vendorimport.go's locateBoundTranscript cannot serve, because kiro has no
// per-session FILE to bind: every conversation lives as one row in a single
// per-user sqlite db (kiroimporter's package doc). Two resolution routes:
//
//  1. e.SessionID, when bound, IS the kiro conversation_id — kiro-cli's own
//     agentSpawn hook maps to the same `ctxloom hook session-bind` target
//     codex/claude use (internal/kiro/settings.go's mapHooks, u.SessionStart
//     -> AgentSpawn), so a payload that DOES carry a usable id lands here
//     exactly like any other bind, no enumeration needed.
//  2. No SessionID bound — the situation this repo's own dogfood
//     .kiro/agents/ctxloom.json shows today (the agentSpawn hook IS wired,
//     but whether kiro-cli's payload carries a real conversation id this
//     early — before the conversation itself may even exist — is
//     UNCONFIRMED, unlike Codex's/Claude's SessionStart payload, which is
//     live-verified to carry one; flagged in the wiring report, not
//     silently assumed to work). Falls back to
//     kiroimporter.EnumerateConversations and picks the most-recently
//     updated conversation whose WorkDir matches e.ProjectDir and whose
//     UpdatedAt is not older than e.StartedAt: "the conversation kiro-cli
//     itself just finished writing in this project, since this run
//     started." A BEST-EFFORT heuristic, not a guarantee — two concurrent
//     kiro-cli interactive sessions in the SAME project dir, both started
//     within the same run's window, are indistinguishable by this signal.
func locateKiroConversation(ctx context.Context, e sessions.Entry) (string, bool) {
	for _, dbPath := range candidateKiroDBPaths(e.HarpName) {
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		if loc, ok := locateKiroConversationInDB(ctx, dbPath, e); ok {
			return loc, true
		}
	}
	return "", false
}

// candidateKiroDBPaths orders the sqlite db(s) locateKiroConversation
// checks: an isolated (worktree) run's own per-agent db FIRST, then the
// host-ambient one. Dizzy-zoom's second caveat: kiroDBPath() reads only the
// CURRENT PROCESS's own $XDG_DATA_HOME (this host-side, post-exit lookup's
// own env), never an isolated child's per-agent override
// (isolation/worktree.go's provisionConfigHome sets XDG_DATA_HOME to
// <HarpEphemeralDir>/ctxloom-cfg-<agentID>-<randSuffix>/xdg-data for a
// worktree-isolated kiro run) — so an isolated conversation was silently
// invisible to import, indistinguishable from "nothing to convert" even
// though (confirmed by reading run.go: convertVendorTranscriptOnExit runs
// INLINE, before the function returns and its deferred ws.Cleanup() fires)
// the isolated db is still on disk at locate time. The random per-launch
// suffix in that path can't be reconstructed from harp/agent id alone
// (worktreeScratchPath's own doc), so it is instead DISCOVERED by globbing
// the harp's own ephemeral scratch dir — no new plumbing/state needed, and
// nothing to keep in sync if worktree.go's naming ever shifts besides this
// one glob pattern.
func candidateKiroDBPaths(harp string) []string {
	var out []string
	if harp != "" {
		if dir, err := paths.HarpEphemeralDir(harp); err == nil {
			if matches, gerr := filepath.Glob(filepath.Join(dir, "ctxloom-cfg-*", "xdg-data", "kiro-cli", "data.sqlite3")); gerr == nil {
				out = append(out, matches...)
			}
		}
	}
	if host := kiroDBPath(); host != "" {
		out = append(out, host)
	}
	return out
}

// locateKiroConversationInDB resolves e's conversation locator against one
// already-confirmed-to-exist sqlite db: e.SessionID directly when bound
// (kiro's agentSpawn hook sets KIRO_SESSION_ID — see session_cmd.go's
// bindSessionFromPayload — so this is now the common case, not just a
// theoretical fast path), else the enumerate-by-workdir best-effort
// fallback for a pre-bind entry.
func locateKiroConversationInDB(ctx context.Context, dbPath string, e sessions.Entry) (string, bool) {
	if e.SessionID != "" {
		return kiroimporter.Locator(dbPath, e.SessionID), true
	}

	if e.ProjectDir == "" {
		return "", false
	}
	refs, err := kiroimporter.EnumerateConversations(ctx, dbPath)
	if err != nil || len(refs) == 0 {
		return "", false
	}
	var best *kiroimporter.ConversationRef
	for i := range refs {
		r := &refs[i]
		if r.WorkDir != e.ProjectDir {
			continue
		}
		if !e.StartedAt.IsZero() && r.UpdatedAt.Before(e.StartedAt) {
			continue
		}
		if best == nil || r.UpdatedAt.After(best.UpdatedAt) {
			best = r
		}
	}
	if best == nil {
		return "", false
	}
	return kiroimporter.Locator(dbPath, best.ConversationID), true
}

// kiroDBPath resolves kiro-cli's HOST-AMBIENT conversation store:
// $XDG_DATA_HOME/kiro-cli/data.sqlite3, falling back to
// ~/.local/share/kiro-cli/data.sqlite3 when XDG_DATA_HOME is unset — the
// same convention kiroimporter/schema.go's package doc and
// internal/lm/isolation/auth.go's credentialSeedSpecs["kiro"] both document
// independently. Reads the CURRENT PROCESS's own env, which is correct for
// an unisolated (host-mode) kiro run but NOT for an isolated (worktree)
// one — see candidateKiroDBPaths, which checks the isolated per-agent db
// FIRST and falls back to this host-ambient one only when no isolated db
// was found (dizzy-zoom). Returns "" when even $HOME can't be resolved.
func kiroDBPath() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "kiro-cli", "data.sqlite3")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
}
