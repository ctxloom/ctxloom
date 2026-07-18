package operations

import (
	"context"
	"os"
	"path/filepath"

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
	dbPath := kiroDBPath()
	if dbPath == "" {
		return "", false
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", false
	}

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

// kiroDBPath resolves kiro-cli's conversation store:
// $XDG_DATA_HOME/kiro-cli/data.sqlite3, falling back to
// ~/.local/share/kiro-cli/data.sqlite3 when XDG_DATA_HOME is unset — the
// same convention kiroimporter/schema.go's package doc and
// internal/lm/isolation/auth.go's credentialSeedSpecs["kiro"] both document
// independently. Reads the CURRENT PROCESS's own env, not any per-run
// isolated child env — a deliberate, flagged limitation: an isolated
// (worktree) kiro-cli run gets its OWN XDG_DATA_HOME override
// (isolation/worktree.go's per-agent config-home Env(), gated on
// credentials) passed only to the CHILD process's environment, which this
// host-side, post-exit lookup has no way to observe. Returns "" when even
// $HOME can't be resolved.
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
