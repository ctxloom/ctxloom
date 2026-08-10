package operations

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	kiroreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/kiro"
)

// locateKiroConversation resolves kiro's composite "<db-path>#<conversation-id>"
// locator (kiroreader.Locator) for one indexed entry — the one engine
// vendorreader.go's locateBoundTranscript cannot serve, because kiro has no
// per-session FILE to bind: every conversation lives as one row in a single
// per-user sqlite db (kiroreader's package doc). Two resolution routes:
//
//  1. e.SessionID, when bound, IS the kiro conversation_id — kiro-cli's own
//     agentSpawn hook maps to the same `ctxloom hook session-bind` target
//     codex/claude use (internal/kiro/settings.go's mapHooks, u.SessionStart
//     -> AgentSpawn), so a payload that DOES carry a usable id lands here
//     exactly like any other bind, with no per-project matching to derive.
//     The id still has to be paired with the db that holds it — see
//     kiroDBHoldingConversation.
//  2. No SessionID bound — the situation this repo's own dogfood
//     .kiro/agents/ctxloom.json shows today (the agentSpawn hook IS wired,
//     but whether kiro-cli's payload carries a real conversation id this
//     early — before the conversation itself may even exist — is
//     UNCONFIRMED, unlike Codex's/Claude's SessionStart payload, which is
//     live-verified to carry one; flagged in the wiring report, not
//     silently assumed to work). Falls back to
//     kiroreader.EnumerateConversations and picks the most-recently
//     updated conversation whose WorkDir matches e.ProjectDir and whose
//     UpdatedAt is not older than e.StartedAt: "the conversation kiro-cli
//     itself just finished writing in this project, since this run
//     started." A BEST-EFFORT heuristic, not a guarantee — two concurrent
//     kiro-cli interactive sessions in the SAME project dir, both started
//     within the same run's window, are indistinguishable by this signal.
func locateKiroConversation(ctx context.Context, e sessions.Entry) (string, bool) {
	var dbs []string
	for _, dbPath := range candidateKiroDBPaths(e.HarpName) {
		if _, err := os.Stat(dbPath); err == nil {
			dbs = append(dbs, dbPath)
		}
	}
	if len(dbs) == 0 {
		return "", false
	}
	if e.SessionID != "" {
		src, err := kiroreader.Locator(kiroDBHoldingConversation(ctx, dbs, e.SessionID), e.SessionID)
		return src, err == nil
	}
	for _, dbPath := range dbs {
		if loc, ok := locateKiroConversationInDB(ctx, dbPath, e); ok {
			return loc, true
		}
	}
	return "", false
}

// kiroDBHoldingConversation picks which of several candidate dbs a BOUND
// conversation id names. A conversation id is only meaningful relative to the
// db it lives in, so a bound id does not on its own say which candidate to
// read: an isolated (worktree) run's per-agent db and the host-ambient one are
// independent stores, and candidateKiroDBPaths can offer several of the former
// (one per isolated member agent under this harp's ephemeral dir). Handing
// back the first candidate regardless made a host-db conversation unreachable
// for any harp that also had an isolated db on disk — Convert then fails
// against a store that never held the row.
//
// One candidate is answered without touching sqlite at all: with nothing to
// choose between, the bound id IS the answer and reading the db would only add
// a way to fail. A db that cannot be enumerated is skipped and SAID: the next
// candidate may still hold the conversation, so one unreadable store must not
// end the search — but "I cannot read this store" is a different fact from
// "the conversation is not in it", and a silent skip makes the two
// indistinguishable when the search then fails. When no candidate holds the id
// (a db written since the index entry was bound, an id from a store that has
// since been removed), the first candidate is returned so the resulting Convert
// failure names the db the caller would have read anyway.
func kiroDBHoldingConversation(ctx context.Context, dbs []string, conversationID string) string {
	if len(dbs) == 1 {
		return dbs[0]
	}
	for _, dbPath := range dbs {
		refs, err := kiroreader.EnumerateConversations(ctx, dbPath)
		if err != nil {
			clidiag.Warn("ctxloom", "kiro conversation store %s is unreadable (%v); it was skipped while looking for conversation %s", dbPath, err, conversationID)
			continue
		}
		for i := range refs {
			if refs[i].ConversationID == conversationID {
				return dbPath
			}
		}
	}
	return dbs[0]
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

// locateKiroConversationInDB resolves a PRE-BIND entry's conversation locator
// against one already-confirmed-to-exist sqlite db: the enumerate-by-workdir
// best-effort fallback for an entry that carries no SessionID. A bound
// SessionID never reaches here — it names a conversation rather than
// describing one, so its candidate db is chosen by kiroDBHoldingConversation
// instead of by re-deriving a match per db.
func locateKiroConversationInDB(ctx context.Context, dbPath string, e sessions.Entry) (string, bool) {
	if e.ProjectDir == "" {
		return "", false
	}
	refs, err := kiroreader.EnumerateConversations(ctx, dbPath)
	if err != nil {
		// "I cannot read this store" is not "this session has nothing to
		// import", and collapsing the two hid a corrupt, truncated or locked
		// db behind the Skipped count a sweeping import reports for the
		// ordinary nothing-to-do case. The locator still degrades to not-found
		// — one bad store must never abort a whole backfill (CLAUDE.md fault
		// tolerance) — but the reason is stated, and it names the file.
		clidiag.Warn("ctxloom", "kiro conversation store %s is unreadable (%v); no transcript can be imported from it", dbPath, err)
		return "", false
	}
	if len(refs) == 0 {
		return "", false
	}
	var best *kiroreader.ConversationRef
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
	src, err := kiroreader.Locator(dbPath, best.ConversationID)
	return src, err == nil
}

// kiroDBPath resolves kiro-cli's HOST-AMBIENT conversation store:
// $XDG_DATA_HOME/kiro-cli/data.sqlite3, falling back to
// ~/.local/share/kiro-cli/data.sqlite3 when XDG_DATA_HOME is unset — the
// same convention kiroreader/schema.go's package doc and
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
