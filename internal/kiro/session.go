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

// Kiro persists a TTY-driven `kiro-cli chat` session as a per-session tuple
// under KIRO_HOME/sessions/cli/ (default ~/.kiro/sessions/cli/), keyed by a
// UUID session id: {id}.json (metadata: cwd/created_at/updated_at/title —
// read here for workDir scoping), {id}.jsonl (the append-only transcript this
// file parses), {id}.lock (held while the process is live), and {id}.history
// (a flat plain-text log of raw prompt strings, not the structured
// transcript — unused here). Confirmed against real files from a live,
// authenticated kiro-cli 2.12.1: driving both a plain Q&A turn and a
// tool-calling turn through an actual pty and reading back what landed on
// disk.
//
// Each jsonl line is {"version":"v1","kind":<Prompt|AssistantMessage|
// ToolResults>,"data":{...}}. "data.content" is an array of typed blocks —
// {"kind":"text","data":"<string>"}, {"kind":"toolUse","data":{"toolUseId",
// "name","input"}}, or {"kind":"toolResult","data":{"toolUseId","content",
// "status"}} — the speaker lives in the line's "kind", never a flat
// "type"/"role" field, and tool traffic is nested content blocks rather than
// its own flat name/input/output fields. "data.meta.timestamp" (unix
// seconds) is present on Prompt lines only; AssistantMessage/ToolResults
// lines carry none, so their entries timestamp zero.
//
// NOT covered by this reader: a `kiro-cli chat --no-interactive` oneshot (the
// mode ctxloom's own oneshot Execute path uses) does not write here at all —
// live-verified it persists instead into a SQLite blob
// ($XDG_DATA_HOME/kiro-cli/data.sqlite3, table conversations_v2, keyed by
// cwd), a structurally different store this reader does not parse. See
// internal/lm/isolation/auth.go for the credential-store implication of the
// same sqlite file. A oneshot run's history is therefore invisible to
// GetCurrentSession/ListSessions/compaction/recovery; only TTY-interactive
// sessions are readable today.
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

// storeDir resolves the session store root: sessions/cli/ under $KIRO_HOME
// when set (the same variable the worktree isolation policy uses to
// relocate kiro's global state), else under ~/.kiro. The sessions/cli
// descent is required — real session files live one directory deeper than
// KIRO_HOME/~/.kiro itself, which holds agents/settings/skills/steering as
// siblings of sessions/.
func (h *kiroSessionHistory) storeDir() (string, error) {
	if v := h.getenv("KIRO_HOME"); v != "" {
		return filepath.Join(v, "sessions", "cli"), nil
	}
	home, err := h.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kiro", "sessions", "cli"), nil
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

// kiroLine is one jsonl transcript line: {"version":"v1","kind":<Prompt|
// AssistantMessage|ToolResults>,"data":{...}}. The speaker/event lives in
// Kind, never a flat "type"/"role" field.
type kiroLine struct {
	Kind string       `json:"kind"`
	Data kiroLineData `json:"data"`
}

// kiroLineData is the "data" payload shared by all three line kinds: a list
// of typed content blocks, plus an optional meta.timestamp. Meta is present
// on Prompt lines (unix seconds); AssistantMessage/ToolResults lines carry
// none in a real transcript, so their entries timestamp zero rather than
// guess.
type kiroLineData struct {
	Content []kiroContentBlock `json:"content"`
	Meta    *kiroLineMeta      `json:"meta"`
}

type kiroLineMeta struct {
	Timestamp int64 `json:"timestamp"`
}

// kiroContentBlock is one typed content block. Kind names the block type
// ("text", "toolUse", "toolResult"); Data is that type's own payload, decoded
// separately by kind since the three shapes are unrelated (a string, a tool
// call, a tool outcome).
type kiroContentBlock struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// kiroToolUse is a "toolUse" block's data: the tool call an AssistantMessage
// is making. ToolUseID correlates it to the ToolResults line that follows,
// but that correlation is NOT resolved here — parseKiroLine sees one jsonl
// line at a time with no cross-line state, so a resulting tool_result entry
// carries no ToolName (see kiroToolResultEntries).
type kiroToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// kiroToolResult is a "toolResult" block's data: the completed call's
// outcome, keyed back to its toolUse by ToolUseID. Status is "success" or an
// error status string.
type kiroToolResult struct {
	ToolUseID string             `json:"toolUseId"`
	Content   []kiroContentBlock `json:"content"`
	Status    string             `json:"status"`
}

// parseKiroLine converts one jsonl line into zero or more normalized entries.
// Unknown kinds/shapes yield nothing — the transcript degrades to what is
// recognizable rather than erroring (the ParseSessionFile contract).
func parseKiroLine(line []byte) []agent.SessionEntry {
	var raw kiroLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	var ts time.Time
	if raw.Data.Meta != nil {
		ts = time.Unix(raw.Data.Meta.Timestamp, 0)
	}

	switch raw.Kind {
	case "Prompt":
		if text := kiroBlocksText(raw.Data.Content); text != "" {
			return []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: text, Timestamp: ts}}
		}
	case "AssistantMessage":
		return kiroAssistantEntries(raw.Data.Content, ts)
	case "ToolResults":
		return kiroToolResultEntries(raw.Data.Content, ts)
	}
	return nil
}

// kiroAssistantEntries converts an AssistantMessage's content blocks into an
// assistant text entry (when the text block is non-empty — a tool-only turn
// carries an empty "text" block alongside its toolUse blocks) followed by one
// tool_use entry per toolUse block.
func kiroAssistantEntries(blocks []kiroContentBlock, ts time.Time) []agent.SessionEntry {
	var entries []agent.SessionEntry
	if text := kiroBlocksText(blocks); text != "" {
		entries = append(entries, agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: text, Timestamp: ts})
	}
	for _, b := range blocks {
		if b.Kind != "toolUse" {
			continue
		}
		var tu kiroToolUse
		if err := json.Unmarshal(b.Data, &tu); err != nil {
			continue
		}
		entries = append(entries, agent.SessionEntry{
			Type:      agent.EntryTypeToolUse,
			ToolName:  tu.Name,
			ToolInput: tu.Input,
			Timestamp: ts,
		})
	}
	return entries
}

// kiroToolResultEntries converts a ToolResults line's content blocks into one
// tool_result entry per toolResult block. ToolName is left empty: a real
// toolResult payload carries only toolUseId, not the tool name, and
// resolving it would need the matching toolUse from a prior line that this
// per-line parser never sees.
func kiroToolResultEntries(blocks []kiroContentBlock, ts time.Time) []agent.SessionEntry {
	var entries []agent.SessionEntry
	for _, b := range blocks {
		if b.Kind != "toolResult" {
			continue
		}
		var tr kiroToolResult
		if err := json.Unmarshal(b.Data, &tr); err != nil {
			continue
		}
		entries = append(entries, agent.SessionEntry{
			Type:       agent.EntryTypeToolResult,
			ToolOutput: kiroBlocksText(tr.Content),
			IsError:    tr.Status != "" && tr.Status != "success",
			Timestamp:  ts,
		})
	}
	return entries
}

// kiroBlocksText concatenates every "text"-kind block's string data. Used for
// Prompt/AssistantMessage text and for a toolResult's own nested content.
func kiroBlocksText(blocks []kiroContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Kind != "text" {
			continue
		}
		var s string
		if err := json.Unmarshal(b.Data, &s); err == nil && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}
