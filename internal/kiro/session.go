package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Kiro persists sessions as a per-session triple under KIRO_HOME (default
// ~/.kiro): {session_id}.json (metadata: cwd/timestamps/state), a
// {session_id}.jsonl append-only conversation log, and a {session_id}.lock.
// Sessions are scoped per working directory via the metadata's cwd.
//
// CONFIRMED BROKEN against a live, authenticated kiro-cli — not merely
// unverified. Two independent defects, either one alone sufficient: (1) real
// session files live one directory deeper than storeDir() looks —
// KIRO_HOME/sessions/cli/{id}.json + {id}.jsonl, not directly under
// KIRO_HOME — so ListSessions's afero.ReadDir(dir) sees only KIRO_HOME's
// agents/settings/skills/steering/sessions subdirectories and finds zero
// real session files, regardless of line shape. (2) the jsonl line shape
// audited here is also wrong: a real line is
// {"version":"v1","kind":"Prompt"|"AssistantMessage",...,"data":{"content":
// [{"kind":"text","data":"..."}],...}} — the text nested under "data", the
// speaker under "kind" — not the flat {"type"/"role","content","timestamp"}
// shape parseKiroLine expects, so a real line handed to it directly still
// resolves to an empty speaker and parses to zero entries (degrading
// silently per the defensive-parse contract, never erroring — which is
// exactly why this went unnoticed rather than failing loudly). Fixing this
// needs storeDir() to descend into sessions/cli/ AND parseKiroLine rewritten
// for the real "kind"/"data"-wrapped shape; neither is done here.
//
// TODO(phase-3 remainder): map harp names → session ids (Kiro has no --name;
// use --resume-id / --list-sessions --format json).

// kiroSessionHistory implements agent.SessionHistory over the Kiro session
// store. The embedded agent.SessionStore carries the afero fs + homeDir
// injection points and the shared transcript parse loop; getenv is the
// KIRO_HOME override seam for tests.
type kiroSessionHistory struct {
	agent.SessionStore
	getenv func(string) string
}

// newKiroSessionHistory builds the session reader over the OS filesystem.
func newKiroSessionHistory() *kiroSessionHistory {
	return &kiroSessionHistory{SessionStore: agent.NewSessionStore(), getenv: os.Getenv}
}

// storeDir resolves the session store root: $KIRO_HOME when set (the same
// variable the worktree isolation policy uses to relocate kiro's global
// state), else ~/.kiro.
func (h *kiroSessionHistory) storeDir() (string, error) {
	if v := h.getenv("KIRO_HOME"); v != "" {
		return v, nil
	}
	home, err := h.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kiro"), nil
}

// kiroSessionMeta is the {session_id}.json metadata sidecar (doc-first keys;
// absent/renamed fields degrade to zero values — the listing then falls back
// to file mtimes).
type kiroSessionMeta struct {
	Cwd       string `json:"cwd"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// GetCurrentSession returns the most recent session transcript for workDir.
func (h *kiroSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	sessions, err := h.ListSessions(workDir)
	return agent.MostRecentSession(sessions, err, func(m agent.SessionMeta) (*agent.Session, error) {
		return h.GetSessionByPath(m.Path)
	})
}

// ListSessions returns the sessions whose metadata cwd matches workDir, most
// recent first. A session with no readable metadata sidecar is skipped for a
// workDir-scoped listing (its directory affinity is unknowable); an empty
// workDir lists everything.
func (h *kiroSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	dir, err := h.storeDir()
	if err != nil {
		return nil, err
	}
	fs := agent.GetFS(h.FS)
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		return nil, err // no store dir → no sessions (callers treat as absence)
	}

	absWork := ""
	if workDir != "" {
		if absWork, err = filepath.Abs(workDir); err != nil {
			return nil, err
		}
	}

	var sessions []agent.SessionMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		meta, ok := h.readMeta(filepath.Join(dir, id+".json"))
		if absWork != "" && (!ok || meta.Cwd != absWork) {
			continue
		}
		start := entry.ModTime() // fallback when the sidecar carries no time
		if t, terr := time.Parse(time.RFC3339, meta.CreatedAt); terr == nil {
			start = t
		}
		sessions = append(sessions, agent.SessionMeta{
			ID:        id,
			StartTime: start,
			Path:      filepath.Join(dir, entry.Name()),
		})
	}
	agent.SortSessionsMostRecentFirst(sessions)
	return sessions, nil
}

// readMeta loads a session's metadata sidecar; ok=false when it is absent or
// unparsable.
func (h *kiroSessionHistory) readMeta(path string) (kiroSessionMeta, bool) {
	data, err := afero.ReadFile(agent.GetFS(h.FS), path)
	if err != nil {
		return kiroSessionMeta{}, false
	}
	var m kiroSessionMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return kiroSessionMeta{}, false
	}
	return m, true
}

// GetSession returns a specific session by ID.
func (h *kiroSessionHistory) GetSession(workDir, sessionID string) (*agent.Session, error) {
	dir, err := h.storeDir()
	if err != nil {
		return nil, err
	}
	return h.GetSessionByPath(filepath.Join(dir, sessionID+".jsonl"))
}

// GetSessionByPath parses one session jsonl via the shared transcript loop;
// malformed lines are skipped (degrade to a partial transcript).
func (h *kiroSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return h.ParseSessionFile(path, id, parseKiroLine)
}

// TranscriptPathFromHook returns the hook-supplied transcript path directly
// (the codex/antigravity convention — kiro's agent-JSON hooks are expected to
// carry it; pending live verification).
func (h *kiroSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}

// kiroLine is one jsonl log line, read defensively: role/type name the
// speaker, content is a string or an array of typed blocks, timestamp is
// RFC3339 when present.
type kiroLine struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Timestamp string          `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
	// Flat tool fields, for lines that record tool traffic directly.
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Output  string          `json:"output"`
	IsError bool            `json:"is_error"`
}

// kiroBlock is one typed content block ({"type":"text","text":...} style).
type kiroBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseKiroLine converts one jsonl line into zero or more normalized entries.
// Unknown speakers/shapes yield nothing — the transcript degrades to what is
// recognizable rather than erroring (the ParseSessionFile contract).
func parseKiroLine(line []byte) []agent.SessionEntry {
	var raw kiroLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	speaker := raw.Role
	if speaker == "" {
		speaker = raw.Type
	}
	var ts time.Time
	if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
		ts = t
	}

	switch speaker {
	case "user", "human", "prompt":
		if text := kiroContentText(raw.Content); text != "" {
			return []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: text, Timestamp: ts}}
		}
	case "assistant", "response":
		if text := kiroContentText(raw.Content); text != "" {
			return []agent.SessionEntry{{Type: agent.EntryTypeAssistant, Content: text, Timestamp: ts}}
		}
	case "tool_use", "tool":
		return []agent.SessionEntry{{Type: agent.EntryTypeToolUse, ToolName: raw.Name, ToolInput: raw.Input, Timestamp: ts}}
	case "tool_result":
		return []agent.SessionEntry{{Type: agent.EntryTypeToolResult, ToolName: raw.Name, ToolOutput: raw.Output, IsError: raw.IsError, Timestamp: ts}}
	}
	return nil
}

// kiroContentText flattens a content field: a JSON string passes through, an
// array of blocks concatenates its text blocks.
func kiroContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []kiroBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
