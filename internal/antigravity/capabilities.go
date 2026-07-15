package antigravity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// antigravitySkillsDir is the workspace skills directory agy reads, relative
// to the workspace root.
const antigravitySkillsDir = AgentsDir + "/skills"

// antigravityManifest tracks ctxloom-written COMMAND files for clean removal,
// distinct from antigravitySkillManifest (skillfiles.go) so the two surfaces'
// cleanup never collides — mirrors kiro's kiroManifest / kiroSkillManifest
// split (kiro/surfaces.go, kiro/skillfiles.go).
const antigravityManifest = ".ctxloom-manifest"

// AntigravityCommands writes commands for Antigravity CLI as Agent Skill
// directories under .agents/skills/.
type AntigravityCommands struct{}

// RegisterFromContent writes skill files from host-resolved command exports.
// The host maps bundle content (with antigravity enablement + metadata) to
// these agent-agnostic exports, so this stays config/bundle-free.
func (s *AntigravityCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// WriteCommandFiles writes enabled command exports as agy Agent Skill
// directories (.agents/skills/<name>/SKILL.md, generated YAML frontmatter) and
// records them in the manifest. Previously manifest-listed files are removed
// first, so the written set always mirrors the current exports (see
// agent.WriteManagedCommandFiles for the shared mechanics; the directory is
// shared with user-authored skills, never wiped wholesale).
//
// G3 FIX (was the silent no-op): this writer used to emit flat `<name>.md`
// files, which agy's skill scanner NEVER discovers — agy only walks
// `.agents/skills/<name>/SKILL.md` DIRECTORIES (VERIFIED against agy's own
// bundled docs, see skillfiles.go's doc comment; every builtin skill is a
// dir). ctxloom slash-command exports landed on disk but were invisible to
// agy. Fixed by rendering the SAME `<name>/SKILL.md` shape kiro already used —
// agent.RenderCommandAsSkillFile is the shared renderer both engines' writers
// call (reprise flagged the two as byte-for-byte duplicates when this was a
// local copy), so the two engines' generated frontmatter can't silently drift
// apart.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(antigravitySkillsDir))
	return agent.WriteManagedCommandFiles(fs, skillsDir, antigravityManifest, cmds,
		agent.RenderCommandAsSkillFile,
		agent.WithManifestTrailingNewline())
}

// agy's command/skill name-collision guard: agy has ONE native directory,
// .agents/skills/<name>/, that would serve BOTH a command rendered as
// SKILL.md (this file) and a true Agent Skill package (skillfiles.go) of the
// same name — both now want .agents/skills/<name>/SKILL.md. Resolved as
// SKILL-WINS by the shared agent.FilterCommandsClaimedBySkills (invoked from
// surfaces.go's NewSurfaces via agent.NewSkillShapedCommandsAndSkills — kiro's
// identical D6 resolution uses the same helper): a name claimed by an enabled
// skill is dropped from the commands set before WriteCommandFiles ever sees
// it, so antigravityManifest (commands) and antigravitySkillManifest (skills)
// never both claim the same path — a later re-materialize (skill
// disabled/removed) lets the command reclaim the name cleanly, and neither
// writer's cleanup can strand the other's live file.

// AntigravitySessionHistory implements SessionHistory for Antigravity CLI.
// Reads from ~/.gemini/antigravity-cli/brain/<conversation-uuid>/
// .system_generated/logs/transcript_full.jsonl. The embedded
// agent.SessionStore carries the afero fs + homeDir injection points used by
// tests and the shared transcript parse loop.
type AntigravitySessionHistory struct {
	backend *Antigravity
	agent.SessionStore
}

// AntigravitySessionHistoryOption configures AntigravitySessionHistory.
type AntigravitySessionHistoryOption func(*AntigravitySessionHistory)

// WithAntigravitySessionFS sets a custom filesystem for testing.
func WithAntigravitySessionFS(fs afero.Fs) AntigravitySessionHistoryOption {
	return func(h *AntigravitySessionHistory) { h.FS = fs }
}

// WithAntigravitySessionHomeDir sets a custom home directory for testing.
func WithAntigravitySessionHomeDir(dir string) AntigravitySessionHistoryOption {
	return func(h *AntigravitySessionHistory) { h.HomeDir = dir }
}

// NewAntigravitySessionHistory creates a new Antigravity session history handler.
func NewAntigravitySessionHistory(backend *Antigravity, opts ...AntigravitySessionHistoryOption) *AntigravitySessionHistory {
	h := &AntigravitySessionHistory{
		backend:      backend,
		SessionStore: agent.NewSessionStore(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// brainDir returns agy's conversation store root.
func (h *AntigravitySessionHistory) brainDir() (string, error) {
	homeDir, err := h.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain"), nil
}

// transcriptPathFor returns the transcript path inside one brain conversation
// directory.
func transcriptPathFor(brainDir, conversationID string) string {
	return filepath.Join(brainDir, conversationID, ".system_generated", "logs", "transcript_full.jsonl")
}

// lastConversationsPath returns agy's workspace -> conversation-UUID map file
// (VERIFIED shape: ~/.gemini/antigravity-cli/cache/last_conversations.json, a
// flat JSON object keyed by the ABSOLUTE workspace path agy was invoked
// against). Sibling cache/projects.json maps workspace -> PROJECT uuid — a
// different id, not the conversation the transcript path needs.
func (h *AntigravitySessionHistory) lastConversationsPath() (string, error) {
	homeDir, err := h.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "cache", "last_conversations.json"), nil
}

// lastConversations reads agy's workspace -> conversation-UUID map. Returns a
// nil map (not an error) when the file is simply absent — a fresh install, or
// a workspace agy has never been invoked against, which callers treat as "no
// entry" and fall back to the global mtime-newest heuristic. A file that
// EXISTS but fails to parse is a real problem (agy changed the format, or the
// file is corrupt) and is surfaced as an error rather than silently
// discarded — proceeding on the mtime-newest fallback there could hand back
// the WRONG workspace's transcript with no indication anything was amiss.
func (h *AntigravitySessionHistory) lastConversations() (map[string]string, error) {
	path, err := h.lastConversationsPath()
	if err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(agent.GetFS(h.FS), path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read last_conversations.json: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse last_conversations.json: %w", err)
	}
	return m, nil
}

// GetCurrentSession returns workDir's current session transcript.
//
// G-session fix: agy's brain store is global (not itself keyed by
// workspace), but agy separately maintains cache/last_conversations.json — an
// exact workDir -> conversation-UUID index (VERIFIED shape, see
// lastConversationsPath) — which this now prefers. Any OTHER agy run in a
// different workspace can no longer win just by having a newer mtime, the bug
// this fix closes. Only when workDir is empty or absent from the map does
// GetCurrentSession fall back to the previous global mtime-newest behavior
// (the same trade-off codex's global session store makes), so an unmapped
// workspace still gets a best-effort answer instead of an error.
func (h *AntigravitySessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	if workDir != "" {
		m, err := h.lastConversations()
		if err != nil {
			return nil, err
		}
		if id, ok := m[filepath.Clean(workDir)]; ok {
			return h.GetSession(workDir, id)
		}
	}
	// Unmapped workDir: fall back to the shared ListSessions+MostRecentSession
	// tail every SessionHistory uses (agent.GetCurrentSessionViaListSessions;
	// claude's and kiro's GetCurrentSession are a bare call to it — antigravity
	// only differs by the workspace-map lookup above it).
	return agent.GetCurrentSessionViaListSessions(workDir, h.ListSessions, func(m agent.SessionMeta) (*agent.Session, error) {
		return h.parseSessionFile(m.Path, m.ID)
	})
}

// ListSessions returns available session metadata, most recent first.
func (h *AntigravitySessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	brain, err := h.brainDir()
	if err != nil {
		return nil, err
	}

	fs := agent.GetFS(h.FS)
	entries, err := afero.ReadDir(fs, brain)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read brain directory: %w", err)
	}

	var sessions []agent.SessionMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := transcriptPathFor(brain, entry.Name())
		info, err := fs.Stat(path)
		if err != nil {
			continue // conversation without a transcript (e.g. still starting)
		}
		sessions = append(sessions, agent.SessionMeta{
			ID:        entry.Name(),
			StartTime: info.ModTime(), // Approximate
			Path:      path,
		})
	}

	agent.SortSessionsMostRecentFirst(sessions)
	return sessions, nil
}

// GetSession returns a specific session by conversation ID.
func (h *AntigravitySessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	brain, err := h.brainDir()
	if err != nil {
		return nil, err
	}
	path := transcriptPathFor(brain, sessionID)
	if _, err := agent.GetFS(h.FS).Stat(path); err != nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	return h.parseSessionFile(path, sessionID)
}

// GetSessionByPath returns a session by its transcript file path.
func (h *AntigravitySessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	// Recover the conversation ID from .../brain/<uuid>/.system_generated/...
	id := ""
	if idx := strings.Index(path, string(filepath.Separator)+".system_generated"+string(filepath.Separator)); idx > 0 {
		id = filepath.Base(path[:idx])
	}
	return h.parseSessionFile(path, id)
}

// TranscriptPathFromHook returns the transcript path agy provides directly on
// every hook's stdin (the transcriptPath field), enabling session-bind.
func (h *AntigravitySessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}

// antigravityTranscriptEntry is one transcript_full.jsonl record. Verified
// shape (agy v1.0.7): {"step_index":N,"source":"USER_EXPLICIT|SYSTEM|MODEL",
// "type":"USER_INPUT|CONVERSATION_HISTORY|PLANNER_RESPONSE|RUN_COMMAND|…",
// "status":"DONE","created_at":RFC3339,"content":"…","tool_calls":[…]}.
type antigravityTranscriptEntry struct {
	StepIndex int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

// parseSessionFile reads an agy transcript into the normalized Session
// contract via the shared SessionStore loop (whose unbounded bufio.Reader
// handles agy's multi-MiB write_to_file lines). Unrecognized records are
// skipped so a session degrades to a partial transcript rather than an error.
func (h *AntigravitySessionHistory) parseSessionFile(path, sessionID string) (*agent.Session, error) {
	return h.ParseSessionFile(path, sessionID, func(line []byte) []agent.SessionEntry {
		var te antigravityTranscriptEntry
		if err := json.Unmarshal(line, &te); err != nil {
			return nil // malformed line — skip
		}
		return convertTranscriptEntry(te)
	})
}

// userRequestRe-equivalent trimming: agy wraps the user's prompt in
// <USER_REQUEST>…</USER_REQUEST> with metadata blocks alongside; extract just
// the request text when present.
func extractUserRequest(content string) string {
	const open, close = "<USER_REQUEST>", "</USER_REQUEST>"
	start := strings.Index(content, open)
	if start < 0 {
		return strings.TrimSpace(content)
	}
	rest := content[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// convertTranscriptEntry maps one agy record to normalized SessionEntries
// (one PLANNER_RESPONSE can carry both text and tool calls). Returns nil for
// records with no conversational content.
func convertTranscriptEntry(te antigravityTranscriptEntry) []agent.SessionEntry {
	var ts time.Time
	if te.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, te.CreatedAt); err == nil {
			ts = t
		}
	}

	var entries []agent.SessionEntry
	switch te.Type {
	case "USER_INPUT":
		if text := extractUserRequest(te.Content); text != "" {
			entries = append(entries, agent.SessionEntry{
				Type: agent.EntryTypeUser, Content: text, Timestamp: ts,
			})
		}
	case "PLANNER_RESPONSE":
		if text := strings.TrimSpace(te.Content); text != "" {
			entries = append(entries, agent.SessionEntry{
				Type: agent.EntryTypeAssistant, Content: text, Timestamp: ts,
			})
		}
		for _, tc := range te.ToolCalls {
			entries = append(entries, agent.SessionEntry{
				Type: agent.EntryTypeToolUse, ToolName: tc.Name, ToolInput: tc.Args, Timestamp: ts,
			})
		}
	default:
		// Tool execution records (RUN_COMMAND, …) carry the result.
		if te.Source == "MODEL" && te.Type != "" && te.Content != "" {
			entries = append(entries, agent.SessionEntry{
				Type: agent.EntryTypeToolResult, ToolName: strings.ToLower(te.Type), ToolOutput: te.Content, Timestamp: ts,
			})
		}
	}
	return entries
}
