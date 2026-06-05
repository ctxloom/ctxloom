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

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/sessions"
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
	enabled := true
	content := &bundles.LoadedContent{
		Name:    skill.Name,
		Content: skill.Content,
	}
	content.LLM.Gemini.Enabled = &enabled
	content.LLM.Gemini.Description = skill.Description
	return WriteGeminiCommandFiles(workDir, []*bundles.LoadedContent{content})
}

// RegisterAll adds multiple skills as Gemini CLI slash commands.
func (s *GeminiSkills) RegisterAll(workDir string, skills []Skill) error {
	var contents []*bundles.LoadedContent
	enabled := true
	for _, skill := range skills {
		content := &bundles.LoadedContent{
			Name:    skill.Name,
			Content: skill.Content,
		}
		content.LLM.Gemini.Enabled = &enabled
		content.LLM.Gemini.Description = skill.Description
		contents = append(contents, content)
	}
	return WriteGeminiCommandFiles(workDir, contents)
}

// RegisterFromContent registers skills from LoadedContent objects.
func (s *GeminiSkills) RegisterFromContent(workDir string, contents []*bundles.LoadedContent) error {
	return WriteGeminiCommandFiles(workDir, contents)
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

// WriteGeminiCommandFiles generates Gemini CLI slash command files from prompts.
// It deletes the .gemini/commands/ctxloom/ directory and regenerates it fresh.
// Only prompts with Gemini.IsEnabled() == true are exported.
func WriteGeminiCommandFiles(workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	fs := ResolveCommandFS(opts...)

	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)

	// Clean slate - remove and recreate
	if err := fs.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove ctxloom commands dir: %w", err)
	}

	// Only create the directory if there are prompts to export.
	if !hasExportableGeminiPrompts(prompts) {
		return nil
	}

	if err := fs.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create ctxloom commands dir: %w", err)
	}

	for _, p := range prompts {
		if !p.LLM.Gemini.IsEnabled() {
			continue // Explicitly disabled
		}
		if err := writeGeminiCommand(fs, appDir, p); err != nil {
			return err
		}
	}

	return nil
}

// hasExportableGeminiPrompts reports whether any prompt is enabled for Gemini.
func hasExportableGeminiPrompts(prompts []*bundles.LoadedContent) bool {
	for _, p := range prompts {
		if p.LLM.Gemini.IsEnabled() {
			return true
		}
	}
	return false
}

// writeGeminiCommand writes one prompt as a Gemini .toml command, creating any
// parent directory needed for a nested prompt name.
func writeGeminiCommand(fs afero.Fs, appDir string, p *bundles.LoadedContent) error {
	tomlData, err := TransformToGeminiCommand(p)
	if err != nil {
		return fmt.Errorf("transform command %s: %w", p.Name, err)
	}

	path := filepath.Join(appDir, p.Name+".toml")
	if dir := filepath.Dir(path); dir != appDir {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create command subdir %s: %w", dir, err)
		}
	}

	if err := afero.WriteFile(fs, path, tomlData, 0644); err != nil {
		return fmt.Errorf("write command %s: %w", p.Name, err)
	}
	return nil
}

// TransformToGeminiCommand converts a ctxloom prompt to Gemini CLI command format.
// It generates a TOML file with prompt and description fields.
// Gemini uses {{args}} for argument injection natively.
func TransformToGeminiCommand(p *bundles.LoadedContent) ([]byte, error) {
	cmd := GeminiCommand{
		Description: p.LLM.Gemini.Description,
		Prompt:      p.Content,
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

// GetPreviousSession returns the session before the current one for this harp.
//
// Gemini does not persist SessionStart additionalContext into its transcript, so
// the harp self-id marker that Claude scans for never lands there. Harp
// ownership is instead resolved from the ctxloom session index: `ctxloom session
// bind` records the active harp's session_id and transcript_path (both present
// in Gemini's SessionStart hook payload), so the previous session is the most
// recent prior index entry for this project that carries a readable transcript.
// When the active harp is unknown or the index has no prior bound entry
// (pre-binding history), resolution falls back to the positional datetime floor.
func (h *GeminiSessionHistory) GetPreviousSession(workDir string) (*Session, error) {
	if harp := os.Getenv("CTXLOOM_SESSION_HARP"); harp != "" {
		if mgr, err := sessions.Open(""); err == nil {
			if s, err := previousSessionByIndex(workDir, harp, mgr.ListForProject, h.GetSessionByPath); err == nil && s != nil {
				return s, nil
			}
		}
	}
	return previousSessionByListing(workDir, "", h.ListSessions, h.GetSessionByPath, nil)
}

// previousSessionByIndex resolves the session before the current one using the
// ctxloom session index rather than transcript content — the deterministic path
// for backends (e.g. Gemini) whose transcripts carry no harp self-id marker.
// list returns a project's entries most-recent-first; the active harp's own
// entry is skipped and the first prior entry with a readable transcript wins.
// Returns nil without error when no such entry exists, so the caller can fall
// back to the positional floor. This is the index-backed sibling of
// previousSessionByListing (claude_capabilities.go).
func previousSessionByIndex(
	workDir, myHarp string,
	list func(projectDir string) ([]sessions.Entry, error),
	byPath func(string) (*Session, error),
) (*Session, error) {
	entries, err := list(workDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.HarpName == myHarp || e.TranscriptPath == "" {
			continue
		}
		if s, err := byPath(e.TranscriptPath); err == nil && s != nil {
			return s, nil
		}
	}
	return nil, nil
}
