package antigravity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file holds agy's live conversation-id lookup, and nothing else. It is
// NOT a session-history reader: agy's transcript_full.jsonl scraper was deleted
// outright (it mis-keyed the global brain store and could hand back the wrong
// workspace's conversation; the canonical transcript source is
// internal/transcript, fed by the structured Chat driver), and
// Backend.History() returns nil.
//
// agyConversationMap survives that deletion because it answers a different
// question: which agy conversation does THIS workspace currently have? chat.go's
// resolveChatConversationID needs that on every oneshot turn, since `agy -p`
// never prints its conversation id back. It reads from the same agy-owned
// directory tree the deleted scraper did, but never touches a transcript.

// agyConversationMap resolves agy's own workDir -> conversation-UUID cache
// (cache/last_conversations.json). The embedded agent.SessionStore supplies
// only its FS/HomeDir test-injection seam (ResolveHomeDir) — this type never
// parses a transcript.
type agyConversationMap struct {
	agent.SessionStore
}

// newAgyConversationMap builds the conversation-map reader over the OS
// filesystem (a zero-value agent.SessionStore{} — U102-F17 deleted
// NewSessionStore as a redundant "default to the OS fs" mechanism:
// agent.GetFS already defaults a nil Fs to afero.NewOsFs()). Tests override
// the home dir with a plain struct literal.
func newAgyConversationMap() agyConversationMap {
	return agyConversationMap{SessionStore: agent.SessionStore{}}
}

// path returns agy's workspace -> conversation-UUID map file (VERIFIED
// shape: ~/.gemini/antigravity-cli/cache/last_conversations.json, a flat
// JSON object keyed by the ABSOLUTE workspace path agy was invoked against).
// Sibling cache/projects.json maps workspace -> PROJECT uuid — a different
// id, not the conversation id Chat needs.
func (m agyConversationMap) path() (string, error) {
	homeDir, err := m.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "cache", "last_conversations.json"), nil
}

// read reads agy's workspace -> conversation-UUID map. Returns a nil map
// (not an error) when the file is simply absent — a fresh install, or a
// workspace agy has never been invoked against, which resolveChatConversationID
// treats as "no entry" (ok=false). A file that EXISTS but fails to parse is a
// real problem (agy changed the format, or the file is corrupt) and is
// surfaced as an error rather than silently discarded.
func (m agyConversationMap) read() (map[string]string, error) {
	path, err := m.path()
	if err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(agent.GetFS(m.FS), path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read last_conversations.json: %w", err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("failed to parse last_conversations.json: %w", err)
	}
	return out, nil
}
