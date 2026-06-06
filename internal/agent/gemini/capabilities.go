package gemini

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// geminiAppCommandsDir is the subdirectory for ctxloom-managed Gemini commands.
const geminiAppCommandsDir = "ctxloom"

// GeminiCommand represents a Gemini CLI slash command in TOML format.
type GeminiCommand struct {
	Description string `toml:"description,omitempty"`
	Prompt      string `toml:"prompt"`
}

// GeminiLifecycle implements LifecycleHandler for Gemini using hooks.
// Embeds BaseLifecycle for shared implementation.
type GeminiLifecycle struct {
	*BaseLifecycle
	backend *Gemini
}

// NewGeminiLifecycle creates a new Gemini lifecycle handler.
func NewGeminiLifecycle(backend *Gemini) *GeminiLifecycle {
	return &GeminiLifecycle{
		BaseLifecycle: NewBaseLifecycle("gemini", backend.writeSettings),
		backend:       backend,
	}
}

// GeminiMCPManager implements MCPManager for Gemini CLI.
// Embeds BaseMCPManager for shared implementation.
type GeminiMCPManager struct {
	*BaseMCPManager
	backend *Gemini
}

// NewGeminiMCPManager creates a new Gemini MCP manager.
func NewGeminiMCPManager(backend *Gemini) *GeminiMCPManager {
	return &GeminiMCPManager{
		BaseMCPManager: NewBaseMCPManager("gemini", backend.writeSettings),
		backend:        backend,
	}
}

// GeminiContext implements ContextProvider for Gemini using file + hook.
// Embeds BaseContextProvider for shared implementation.
type GeminiContext struct {
	*BaseContextProvider
	backend *Gemini
}

// NewGeminiContext creates a new Gemini context provider.
func NewGeminiContext(backend *Gemini) *GeminiContext {
	return &GeminiContext{
		BaseContextProvider: NewBaseContextProvider(),
		backend:             backend,
	}
}

// GeminiSkills implements SkillRegistry for Gemini CLI using slash commands.
type GeminiSkills struct {
	backend *Gemini
}

// Register adds a skill as a Gemini CLI slash command.
func (s *GeminiSkills) Register(workDir string, skill Skill) error {
	return WriteCommandFiles(workDir, []agent.CommandExport{skillExport(skill)})
}

// RegisterAll adds multiple skills as Gemini CLI slash commands.
func (s *GeminiSkills) RegisterAll(workDir string, skills []Skill) error {
	cmds := make([]agent.CommandExport, 0, len(skills))
	for _, skill := range skills {
		cmds = append(cmds, skillExport(skill))
	}
	return WriteCommandFiles(workDir, cmds)
}

// RegisterFromContent writes slash commands from host-resolved command exports.
// The host maps bundle content (with gemini enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *GeminiSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// skillExport maps a Skill to an enabled command export.
func skillExport(skill Skill) agent.CommandExport {
	return agent.CommandExport{
		Name:        skill.Name,
		Content:     skill.Content,
		Enabled:     true,
		Description: skill.Description,
	}
}

// Clear removes all ctxloom-managed skills.
func (s *GeminiSkills) Clear(workDir string) error {
	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)
	return os.RemoveAll(appDir)
}

// List returns registered skill names.
func (s *GeminiSkills) List(workDir string) ([]string, error) {
	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)
	entries, err := os.ReadDir(appDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".toml" {
			name := entry.Name()[:len(entry.Name())-5] // Remove .toml
			names = append(names, name)
		}
	}
	return names, nil
}

// WriteCommandFiles generates Gemini CLI slash command files from command
// exports. It deletes the .gemini/commands/ctxloom/ directory and regenerates
// it fresh (the directory is ctxloom-owned, so a wipe is safe). Only exports
// with Enabled == true are written.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)

	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)

	// Clean slate - remove and recreate
	if err := fs.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove ctxloom commands dir: %w", err)
	}

	// Only create the directory if there are exports to write.
	if !hasExportableCommands(cmds) {
		return nil
	}

	if err := fs.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create ctxloom commands dir: %w", err)
	}

	for _, c := range cmds {
		if !c.Enabled {
			continue
		}
		if err := writeGeminiCommand(fs, appDir, c); err != nil {
			return err
		}
	}

	return nil
}

// hasExportableCommands reports whether any export is enabled.
func hasExportableCommands(cmds []agent.CommandExport) bool {
	for _, c := range cmds {
		if c.Enabled {
			return true
		}
	}
	return false
}

// writeGeminiCommand writes one export as a Gemini .toml command, creating any
// parent directory needed for a nested name.
func writeGeminiCommand(fs afero.Fs, appDir string, c agent.CommandExport) error {
	tomlData, err := TransformToGeminiCommand(c)
	if err != nil {
		return fmt.Errorf("transform command %s: %w", c.Name, err)
	}

	path := filepath.Join(appDir, c.Name+".toml")
	if dir := filepath.Dir(path); dir != appDir {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create command subdir %s: %w", dir, err)
		}
	}

	if err := afero.WriteFile(fs, path, tomlData, 0644); err != nil {
		return fmt.Errorf("write command %s: %w", c.Name, err)
	}
	return nil
}

// TransformToGeminiCommand converts a command export to Gemini CLI command
// format: a TOML file with prompt and description fields. Gemini uses {{args}}
// for argument injection natively.
func TransformToGeminiCommand(c agent.CommandExport) ([]byte, error) {
	cmd := GeminiCommand{
		Description: c.Description,
		Prompt:      c.Content,
	}

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(cmd); err != nil {
		return nil, fmt.Errorf("encode TOML: %w", err)
	}

	return buf.Bytes(), nil
}

// GeminiSessionHistory implements SessionHistory for Gemini CLI.
// Reads from ~/.gemini/tmp/<hash>/chats/*.json
type GeminiSessionHistory struct {
	backend *Gemini
	fs      afero.Fs
	homeDir string // Override home directory for testing
}

// GeminiSessionHistoryOption configures GeminiSessionHistory.
type GeminiSessionHistoryOption func(*GeminiSessionHistory)

// WithGeminiSessionFS sets a custom filesystem for testing.
func WithGeminiSessionFS(fs afero.Fs) GeminiSessionHistoryOption {
	return func(h *GeminiSessionHistory) {
		h.fs = fs
	}
}

// WithGeminiSessionHomeDir sets a custom home directory for testing.
func WithGeminiSessionHomeDir(dir string) GeminiSessionHistoryOption {
	return func(h *GeminiSessionHistory) {
		h.homeDir = dir
	}
}

// NewGeminiSessionHistory creates a new Gemini session history handler.
func NewGeminiSessionHistory(backend *Gemini, opts ...GeminiSessionHistoryOption) *GeminiSessionHistory {
	h := &GeminiSessionHistory{
		backend: backend,
		fs:      afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *GeminiSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	// Find most recent chat file
	chatsDir := filepath.Join(projectDir, "chats")
	sessions, err := h.listChatFiles(chatsDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found in %s", chatsDir)
	}

	// Return most recent (Path carries the real extension: .jsonl or .json).
	return h.parseSessionFile(sessions[0].Path)
}

// ListSessions returns available session metadata.
func (h *GeminiSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	chatsDir := filepath.Join(projectDir, "chats")
	return h.listChatFiles(chatsDir)
}

// GetSession returns a specific session by ID. Gemini transcript filenames are
// session-<timestamp>-<shortid>, while the real session UUID (what the index
// binds and the compactor looks up) lives in the transcript header. So it
// matches the filename stem first (cheap), then falls back to the header
// sessionId, then to a full parse for the legacy whole-file form whose id is
// only recoverable after parsing.
func (h *GeminiSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	metas, err := h.ListSessions(workDir)
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if m.ID == sessionID {
			return h.parseSessionFile(m.Path)
		}
	}
	for _, m := range metas {
		if h.headerSessionID(m.Path) == sessionID {
			return h.parseSessionFile(m.Path)
		}
	}
	// Legacy whole-file transcripts carry sessionId only inside the JSON object;
	// fall back to a full parse and compare the resolved ID.
	for _, m := range metas {
		if s, err := h.parseSessionFile(m.Path); err == nil && s.ID == sessionID {
			return s, nil
		}
	}
	return nil, fmt.Errorf("session %s not found", sessionID)
}

// headerSessionID reads the sessionId from a JSONL transcript's header line
// without parsing the whole file. Returns "" if unreadable, absent, or if the
// first record is not a header (e.g. the legacy whole-file form).
func (h *GeminiSessionHistory) headerSessionID(path string) string {
	f, err := h.fs.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var hdr struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(line, &hdr) == nil {
			return hdr.SessionID
		}
		return ""
	}
	return ""
}

// GetSessionByPath returns a session by its transcript file path.
func (h *GeminiSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return h.parseSessionFile(path)
}

// findProjectDir finds the Gemini project directory for the given workDir.
//
// Current Gemini stores transcripts under ~/.gemini/tmp/<slug>/ where <slug> is
// a slugified project basename, with the real absolute path recorded in a
// sibling .project_root file. The directory name is not derivable from the path
// (it is a slug, and older Gemini used sha256(path)), so resolution reads
// .project_root and matches the absolute path — robust across layout changes.
// A sha256(path) lookup remains as a fallback for pre-slug transcript stores.
func (h *GeminiSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir := h.homeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
	}

	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	tmpDir := filepath.Join(homeDir, ".gemini", "tmp")

	if dir := h.scanProjectRoots(tmpDir, absPath); dir != "" {
		return dir, nil
	}

	// Legacy fallback: pre-slug Gemini named the dir sha256(absPath).
	legacy := filepath.Join(tmpDir, geminiPathHash(absPath))
	if _, err := h.fs.Stat(legacy); err == nil {
		return legacy, nil
	}

	return "", fmt.Errorf("project directory not found under %s for %s", tmpDir, absPath)
}

// scanProjectRoots returns the tmp subdirectory whose .project_root file records
// absPath, or "" if none matches (or the tmp dir is unreadable).
func (h *GeminiSessionHistory) scanProjectRoots(tmpDir, absPath string) string {
	entries, err := afero.ReadDir(h.fs, tmpDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := afero.ReadFile(h.fs, filepath.Join(tmpDir, e.Name(), ".project_root"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == absPath {
			return filepath.Join(tmpDir, e.Name())
		}
	}
	return ""
}

// geminiPathHash is the legacy pre-slug project-directory name: sha256 of the
// absolute project path, hex-encoded.
func geminiPathHash(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(h[:])
}

// listChatFiles returns session metadata for all chat files, sorted by time (most recent first).
func (h *GeminiSessionHistory) listChatFiles(chatsDir string) ([]SessionMeta, error) {
	entries, err := afero.ReadDir(h.fs, chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read chats directory: %w", err)
	}

	var sessions []SessionMeta
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		// Current Gemini writes .jsonl transcripts; older Gemini wrote .json.
		if ext != ".jsonl" && ext != ".json" {
			continue
		}

		sessions = append(sessions, SessionMeta{
			ID:        strings.TrimSuffix(name, ext),
			StartTime: entry.ModTime(), // Approximate
			Path:      filepath.Join(chatsDir, name),
		})
	}

	// Sort by time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// parseSessionFile reads a Gemini transcript into the normalized Session
// contract. Current Gemini writes JSONL — a header line carrying sessionId,
// then one message object per line; older Gemini wrote a single JSON object
// with a messages array. Both are handled. Unrecognized or contentless lines
// (the interleaved null/typeless records Gemini emits) are skipped so a session
// degrades to a partial transcript rather than an error or an empty result.
func (h *GeminiSessionHistory) parseSessionFile(path string) (*Session, error) {
	data, err := afero.ReadFile(h.fs, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	session := &Session{
		ID:      strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Entries: []SessionEntry{},
	}

	// Legacy whole-file form: {"sessionId": ..., "messages": [ ... ]}.
	var legacy struct {
		SessionID string        `json:"sessionId"`
		Messages  []geminiEntry `json:"messages"`
	}
	if json.Unmarshal(data, &legacy) == nil && len(legacy.Messages) > 0 {
		if legacy.SessionID != "" {
			session.ID = legacy.SessionID
		}
		for _, m := range legacy.Messages {
			if e := h.convertEntry(m); e != nil {
				session.Entries = append(session.Entries, *e)
			}
		}
		h.stampTimes(session)
		return session, nil
	}

	// Current JSONL form: header line followed by one message per line.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ge geminiEntry
		if err := json.Unmarshal(line, &ge); err != nil {
			continue // skip malformed line
		}
		// Header line: carries sessionId and no message type.
		if ge.SessionID != "" && ge.Type == "" && ge.Role == "" {
			session.ID = ge.SessionID
			continue
		}
		if e := h.convertEntry(ge); e != nil {
			session.Entries = append(session.Entries, *e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan session file: %w", err)
	}

	h.stampTimes(session)
	return session, nil
}

// stampTimes sets a session's start/end from its first and last entries.
func (h *GeminiSessionHistory) stampTimes(s *Session) {
	if len(s.Entries) > 0 {
		s.StartTime = s.Entries[0].Timestamp
		s.EndTime = s.Entries[len(s.Entries)-1].Timestamp
	}
}

// geminiEntry is one Gemini transcript record, covering the JSONL header line,
// JSONL message lines, and legacy whole-file messages. content is polymorphic:
// a plain string (gemini/info/error turns) or a [{"text": ...}] array (user
// turns), so it is captured raw and decoded by geminiContentText.
type geminiEntry struct {
	SessionID string          `json:"sessionId"`
	Role      string          `json:"role"` // older/role-keyed transcripts
	Type      string          `json:"type"` // user|gemini|info|error
	Content   json.RawMessage `json:"content"`
	Timestamp string          `json:"timestamp"`
}

// convertEntry maps a Gemini record to a normalized SessionEntry, or nil for
// records that carry no conversational content (header, info, or null lines).
func (h *GeminiSessionHistory) convertEntry(m geminiEntry) *SessionEntry {
	kind := m.Type
	if kind == "" {
		kind = m.Role // tolerate older role-keyed transcripts
	}

	var entryType SessionEntryType
	switch kind {
	case "user", "human":
		entryType = EntryTypeUser
	case "gemini", "model", "assistant":
		entryType = EntryTypeAssistant
	case "error":
		entryType = EntryTypeSystem
	default:
		return nil // info, empty, or unknown — no conversational content
	}

	content := geminiContentText(m.Content)
	if content == "" {
		return nil
	}

	entry := &SessionEntry{Type: entryType, Content: content}
	if m.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}
	return entry
}

// geminiContentText extracts plain text from a Gemini content field, which is
// either a JSON string or an array of {"text": ...} parts. Falls back to the
// raw bytes so an unexpected shape still surfaces something rather than nothing.
func geminiContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

// TranscriptPathFromHook returns the transcript path from hook input.
// For Gemini, the hook provides the path directly.
func (h *GeminiSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}

// "Which session is previous" now lives in ctxloom
// (operations.ResolvePreviousSession), resolved from the session index — exactly
// the index Gemini used to consult here. Moving it out removes the gemini
// package's last dependency on internal/sessions (and its harp/upgrade/filelock
// subtree). This reader only locates, reassembles, and translates a given session.
