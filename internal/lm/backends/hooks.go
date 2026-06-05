package backends

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/spf13/afero"
)

// SettingsWriter is the agent settings-writing contract. It lives in
// internal/agent (the engine-agnostic core) so a consumer can take the settings
// facet without the launch facet (Backend); aliased here for existing sites.
type SettingsWriter = agent.SettingsWriter

// HookWriter is kept for backwards compatibility.
type HookWriter = SettingsWriter

// settingsOptions holds configuration for settings operations.
type settingsOptions struct {
	fs                 afero.Fs
	statusLineDisabled bool
}

// SettingsOption is a functional option for settings operations.
type SettingsOption func(*settingsOptions)

// WithSettingsFS sets the filesystem to use for settings operations.
// If not provided, the real OS filesystem is used.
func WithSettingsFS(fs afero.Fs) SettingsOption {
	return func(o *settingsOptions) {
		o.fs = fs
	}
}

// WithStatusLineDisabled controls whether the ctxloom HUD statusline is managed.
// When disabled, the writer installs no statusline and clears any it previously
// managed, so the user's own (or no) statusline stands.
func WithStatusLineDisabled(disabled bool) SettingsOption {
	return func(o *settingsOptions) {
		o.statusLineDisabled = disabled
	}
}

// WriteSettings writes hooks and MCP servers for the specified backend.
// If the backend doesn't support settings, this is a no-op.
// bundleMCP contains MCP servers resolved from profile bundles.
// Use WithSettingsFS to provide a custom filesystem for testing.
func WriteSettings(backendName string, hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string, opts ...SettingsOption) error {
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}

	writer := newSettingsWriter(backendName, options)
	if writer == nil {
		return nil // Backend doesn't support settings
	}
	return writer.WriteSettings(hooks, mcp, bundleMCP, projectDir)
}

// settingsWriterRegistry maps backend names to their settings writer constructors.
var settingsWriterRegistry = map[string]func(*settingsOptions) SettingsWriter{
	"claude-code": func(o *settingsOptions) SettingsWriter {
		return &ClaudeCodeHookWriter{FS: o.fs, statusLineDisabled: o.statusLineDisabled}
	},
	"gemini": func(o *settingsOptions) SettingsWriter { return &GeminiHookWriter{FS: o.fs} },
}

// newSettingsWriter constructs the named backend's writer from the resolved
// options, or nil if the backend doesn't support settings.
func newSettingsWriter(name string, o *settingsOptions) SettingsWriter {
	if constructor, ok := settingsWriterRegistry[name]; ok {
		return constructor(o)
	}
	return nil
}

// GetSettingsWriter returns a SettingsWriter for the named backend, or nil if not supported.
// If fs is provided, it will be used for filesystem operations; otherwise the OS filesystem is used.
func GetSettingsWriter(name string, fs afero.Fs) SettingsWriter {
	return newSettingsWriter(name, &settingsOptions{fs: fs})
}

// BackendsWithSettings returns the names of all backends that support settings.
func BackendsWithSettings() []string {
	names := make([]string, 0, len(settingsWriterRegistry))
	for name := range settingsWriterRegistry {
		names = append(names, name)
	}
	return names
}

// computeHookHash computes a hash from the hook's defining fields.
func computeHookHash(h config.Hook) string {
	// Create a stable representation for hashing
	parts := []string{
		h.Command,
		h.Matcher,
		h.Type,
		h.Prompt,
		fmt.Sprintf("%d", h.Timeout),
		fmt.Sprintf("%t", h.Async),
	}
	data := strings.Join(parts, "|")
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for brevity
}

// =============================================================================
// Shared Helper Functions
// =============================================================================
// These helpers reduce code duplication between ClaudeCodeHookWriter and
// GeminiHookWriter implementations.

// getFS returns the provided filesystem or a default OS filesystem if nil.
func getFS(fs afero.Fs) afero.Fs {
	if fs == nil {
		return afero.NewOsFs()
	}
	return fs
}

// warn outputs a warning message to stderr with ctxloom prefix.
func warn(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ctxloom: warning: "+format+"\n", args...)
}

// atomicWriteFile writes data to a file atomically with backup.
// It creates a backup of existing files before modifying and uses a temp file
// for atomic writes to prevent corruption if interrupted.
func atomicWriteFile(fs afero.Fs, path string, data []byte, desc string) error {
	// Create backup of existing file before modifying
	if exists, _ := afero.Exists(fs, path); exists {
		backupPath := path + ".ctxloom.bak"
		if origData, err := afero.ReadFile(fs, path); err == nil {
			_ = afero.WriteFile(fs, backupPath, origData, 0644)
		}
	}

	// Atomic write: write to temp file first, then rename
	tmpPath := path + ".ctxloom.tmp"
	if err := afero.WriteFile(fs, tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", desc, err)
	}

	// Rename temp file to final path (atomic on most filesystems)
	if err := fs.Rename(tmpPath, path); err != nil {
		// If rename fails (e.g., cross-device), fall back to direct write
		if writeErr := afero.WriteFile(fs, path, data, 0644); writeErr != nil {
			return fmt.Errorf("failed to write %s: %w", desc, writeErr)
		}
		_ = fs.Remove(tmpPath)
	}

	return nil
}

// ClaudeCodeHookWriter writes hooks to Claude Code's settings.json format.
type ClaudeCodeHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// statusLineDisabled opts out of managing the ctxloom HUD statusline.
	statusLineDisabled bool
}

// getFS returns the filesystem to use, defaulting to the OS filesystem.
func (w *ClaudeCodeHookWriter) getFS() afero.Fs {
	return getFS(w.FS)
}

// HooksPath returns the path to Claude Code's settings.json file.
func (w *ClaudeCodeHookWriter) HooksPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "settings.json")
}

// MCPConfigPath returns the path to Claude Code's .mcp.json file.
// Note: MCP servers must be in .mcp.json (not settings.json) for ${CLAUDE_PROJECT_DIR}
// variable expansion to work. See: https://github.com/anthropics/claude-code/issues/4276
func (w *ClaudeCodeHookWriter) MCPConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".mcp.json")
}

// claudeCodeStatusLine represents the statusLine configuration in settings.json.
type claudeCodeStatusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Padding int    `json:"padding,omitempty"`
}

// claudeCodeSettings represents the structure of .claude/settings.json
// Note: MCP servers are now stored in .mcp.json, not here.
type claudeCodeSettings struct {
	Hooks      map[string][]claudeCodeHookMatcher `json:"hooks,omitempty"`
	StatusLine *claudeCodeStatusLine              `json:"statusLine,omitempty"`
	// Preserve other settings (including legacy mcpServers for backwards compat)
	Other map[string]json.RawMessage `json:"-"`
}

// claudeCodeMCPConfig represents the structure of .mcp.json
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
type claudeCodeMCPConfig struct {
	MCPServers map[string]claudeCodeMCPServer `json:"mcpServers,omitempty"`
}

// claudeCodeMCPServer represents an MCP server configuration in Claude Code format.
type claudeCodeMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`      // Working directory for the server
	SCM     string   `json:"_ctxloom,omitempty"` // Marker identifying ctxloom-managed servers
}

// claudeCodeHookMatcher represents a hook matcher entry in Claude Code format.
type claudeCodeHookMatcher struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []claudeCodeHook `json:"hooks"`
}

// claudeCodeHook represents a single hook in Claude Code format.
//
// Note: The SCM field is intentionally NOT serialized to JSON (json:"-").
// Claude Code uses Zod schema validation with .strict() mode when validating
// edits to settings.json, which rejects unknown fields. Instead of relying on
// a marker field, we identify ctxloom-managed hooks by their executable token
// (the command's first word resolves to `ctxloom`) via isCtxloomManaged().
// This is path-agnostic: any `ctxloom <subcommand>` hook is recognized, so the
// callback subcommand can move without breaking detection or cleanup.
// See: claude-code-src/src/utils/settings/validation.ts:193
type claudeCodeHook struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Async   bool   `json:"async,omitempty"`
	SCM     string `json:"-"` // Internal only - not serialized (Claude Code strict schema validation)
}

// WriteSettings implements SettingsWriter for Claude Code.
// Hooks are written to .claude/settings.json
// MCP servers are written to .mcp.json (where variable expansion works)
func (w *ClaudeCodeHookWriter) WriteSettings(hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &config.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)

	// Ensure .claude directory exists
	claudeDir := filepath.Dir(settingsPath)
	if err := fs.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Load existing settings
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing settings: %w", err)
	}

	// Remove old ctxloom-managed hooks from settings
	w.removeCtxloomHooks(settings)

	// Add ctxloom hooks from unified config
	w.addUnifiedHooks(settings, hooks.Unified)

	// Add ctxloom hooks from backend-specific passthrough
	if backendHooks, ok := hooks.Plugins["claude-code"]; ok {
		w.addBackendHooks(settings, backendHooks)
	}

	// Configure statusLine if not already set by the user
	w.ensureStatusLine(settings)

	// Write hooks to settings.json
	if err := w.saveSettings(settingsPath, settings); err != nil {
		return err
	}

	// Write MCP servers to .mcp.json (separate file where variable expansion works)
	return w.writeMCPConfig(projectDir, mcp, bundleMCP)
}

// WriteHooks implements HookWriter for Claude Code (backwards compatible).
func (w *ClaudeCodeHookWriter) WriteHooks(cfg *config.HooksConfig, projectDir string) error {
	return w.WriteSettings(cfg, nil, nil, projectDir)
}

// SettingsPath returns the path to Claude Code's settings.json file.
func (w *ClaudeCodeHookWriter) SettingsPath(projectDir string) string {
	return w.HooksPath(projectDir)
}

// loadSettings loads existing settings.json or returns empty settings.
// This function is fault-tolerant: on parse errors, it logs a warning and
// returns empty settings rather than failing, allowing ctxloom to continue.
func (w *ClaudeCodeHookWriter) loadSettings(path string) (*claudeCodeSettings, error) {
	settings := &claudeCodeSettings{
		Hooks: make(map[string][]claudeCodeHookMatcher),
		Other: make(map[string]json.RawMessage),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}

	// First unmarshal to get all fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Claude Code's settings.json format is undocumented and may change.
		// If we can't parse it, warn but continue with empty settings.
		// This ensures ctxloom doesn't block startup due to schema changes.
		w.warn("failed to parse settings.json (schema may have changed): %v - ctxloom hooks will be added but existing settings may not be preserved", err)
		return settings, nil
	}

	// Extract hooks separately
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			// Hooks format may have changed - warn but continue
			w.warn("failed to parse hooks in settings.json: %v - existing hooks may not be preserved", err)
			// Don't fail - just skip preserving existing hooks
		}
		delete(raw, "hooks")
	}

	// Extract statusLine separately
	if slRaw, ok := raw["statusLine"]; ok {
		var sl claudeCodeStatusLine
		if err := json.Unmarshal(slRaw, &sl); err != nil {
			w.warn("failed to parse statusLine in settings.json: %v", err)
		} else {
			settings.StatusLine = &sl
		}
		delete(raw, "statusLine")
	}

	// Remove mcpServers from settings.json if present (migrating to .mcp.json)
	delete(raw, "mcpServers")

	// Preserve other fields
	settings.Other = raw

	return settings, nil
}

// warn outputs a warning message to stderr.
func (w *ClaudeCodeHookWriter) warn(format string, args ...interface{}) {
	warn(format, args...)
}

// saveSettings writes settings back to settings.json.
// Note: MCP servers are written separately to .mcp.json
//
// This function implements two safety measures for Claude schema resilience:
// 1. Backup: Creates a .bak file before modifying (preserves original on schema changes)
// 2. Atomic write: Writes to temp file first, then renames (prevents corruption)
func (w *ClaudeCodeHookWriter) saveSettings(path string, settings *claudeCodeSettings) error {
	// Build output map starting with preserved fields
	output := make(map[string]interface{})
	for k, v := range settings.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			w.warn("failed to preserve setting %q: %v", k, err)
			continue // Skip corrupted field
		}
		output[k] = val
	}

	// Add hooks if non-empty
	if len(settings.Hooks) > 0 {
		output["hooks"] = settings.Hooks
	}

	// Add statusLine if configured
	if settings.StatusLine != nil {
		output["statusLine"] = settings.StatusLine
	}

	// Note: mcpServers are NOT written here - they go to .mcp.json

	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	return atomicWriteFile(w.getFS(), path, data, "settings")
}

// loadMCPConfig loads existing .mcp.json or returns empty config.
// This function is fault-tolerant: on parse errors, it logs a warning and
// returns empty config rather than failing, allowing ctxloom to continue.
func (w *ClaudeCodeHookWriter) loadMCPConfig(path string) (*claudeCodeMCPConfig, error) {
	mcpConfig := &claudeCodeMCPConfig{
		MCPServers: make(map[string]claudeCodeMCPServer),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpConfig, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, mcpConfig); err != nil {
		// MCP config format may have changed - warn but continue
		w.warn("failed to parse .mcp.json: %v - existing MCP servers may not be preserved", err)
		return mcpConfig, nil
	}

	if mcpConfig.MCPServers == nil {
		mcpConfig.MCPServers = make(map[string]claudeCodeMCPServer)
	}

	return mcpConfig, nil
}

// saveMCPConfig writes MCP config to .mcp.json.
// Uses backup and atomic write for safety (see saveSettings).
func (w *ClaudeCodeHookWriter) saveMCPConfig(path string, mcpConfig *claudeCodeMCPConfig) error {
	data, err := agent.CanonicalJSON(mcpConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal .mcp.json: %w", err)
	}

	return atomicWriteFile(w.getFS(), path, data, ".mcp.json")
}

// writeMCPConfig writes MCP servers to .mcp.json.
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
func (w *ClaudeCodeHookWriter) writeMCPConfig(projectDir string, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer) error {
	mcpPath := w.MCPConfigPath(projectDir)

	// Load existing MCP config
	mcpConfig, err := w.loadMCPConfig(mcpPath)
	if err != nil {
		return fmt.Errorf("failed to load existing .mcp.json: %w", err)
	}

	// Remove old ctxloom-managed MCP servers. We match the same way as
	// hooks: the SCM marker covers bundle/unified servers (arbitrary
	// commands like `npx …`), and isCtxloomManaged covers ctxloom's own
	// auto-registered server even if its marker ever drifts.
	for name, server := range mcpConfig.MCPServers {
		if server.SCM != "" || isCtxloomManaged(server.Command) {
			delete(mcpConfig.MCPServers, name)
		}
	}

	// Add MCP servers
	w.addMCPServersToConfig(mcpConfig, mcp, bundleMCP)

	// Write MCP config back
	return w.saveMCPConfig(mcpPath, mcpConfig)
}

// ensureStatusLine configures the ctxloom HUD statusline if not already set by the user.
// If the user has configured their own statusLine (not ctxloom-managed), it is preserved.
//
// The statusLine is a single dedicated slot, so we recognize ours by the
// executable alone (isCtxloomManaged) — not the verb. Keying on the exact
// `meta hud` path orphaned installs whenever the verb moved (the legacy
// `ctxloom hook hud` form): this saw an unrecognized command, assumed it
// was user-authored, and preserved it. The dead `hook hud` then dumped the
// `hook` help into the status bar on every render. Matching any
// ctxloom-emitted command lets apply migrate it forward.
func (w *ClaudeCodeHookWriter) ensureStatusLine(settings *claudeCodeSettings) {
	// If statusLine is set and NOT ctxloom-managed, respect the user's config
	if settings.StatusLine != nil && !isCtxloomManaged(settings.StatusLine.Command) {
		return
	}

	// Opt-out: don't manage a statusline. Clear any previously ctxloom-managed
	// one so the user ends up with their own choice (or none).
	if w.statusLineDisabled {
		settings.StatusLine = nil
		return
	}

	// Set or update ctxloom-managed statusLine. Bare `ctxloom` resolves
	// via PATH at fire time — see GetExecutablePath for the rationale
	// behind not baking an absolute path into the file.
	settings.StatusLine = &claudeCodeStatusLine{
		Type:    "command",
		Command: ctxloomBinary + " hook hud",
	}
}

// removeCtxloomHooks removes all ctxloom-managed hooks from settings.
// It identifies ctxloom hooks by command pattern: containing "ctxloom" AND "inject-context".
// The SCM field is checked for in-memory hooks but is not serialized to JSON
// (Claude Code uses strict schema validation that rejects unknown fields).
func (w *ClaudeCodeHookWriter) removeCtxloomHooks(settings *claudeCodeSettings) {
	for eventName, matchers := range settings.Hooks {
		var filteredMatchers []claudeCodeHookMatcher
		for _, matcher := range matchers {
			var filteredHooks []claudeCodeHook
			for _, hook := range matcher.Hooks {
				// Keep hooks that are NOT ctxloom-managed
				if hook.SCM == "" && !isCtxloomManaged(hook.Command) {
					filteredHooks = append(filteredHooks, hook)
				}
			}
			if len(filteredHooks) > 0 {
				matcher.Hooks = filteredHooks
				filteredMatchers = append(filteredMatchers, matcher)
			}
		}
		if len(filteredMatchers) > 0 {
			settings.Hooks[eventName] = filteredMatchers
		} else {
			delete(settings.Hooks, eventName)
		}
	}
}

// addUnifiedHooks translates unified hooks to Claude Code format and adds them.
func (w *ClaudeCodeHookWriter) addUnifiedHooks(settings *claudeCodeSettings, unified config.UnifiedHooks) {
	// PreTool -> PreToolUse
	for _, h := range unified.PreTool {
		w.addHook(settings, "PreToolUse", h)
	}

	// PostTool -> PostToolUse
	for _, h := range unified.PostTool {
		w.addHook(settings, "PostToolUse", h)
	}

	// SessionStart -> SessionStart
	for _, h := range unified.SessionStart {
		w.addHook(settings, "SessionStart", h)
	}

	// SessionEnd -> SessionEnd
	for _, h := range unified.SessionEnd {
		w.addHook(settings, "SessionEnd", h)
	}

	// PreShell -> PreToolUse with Bash matcher
	for _, h := range unified.PreShell {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Bash"
		}
		w.addHook(settings, "PreToolUse", hook)
	}

	// PostFileEdit -> PostToolUse with Edit|Write matcher
	for _, h := range unified.PostFileEdit {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Edit|Write"
		}
		w.addHook(settings, "PostToolUse", hook)
	}
}

// addBackendHooks adds backend-specific passthrough hooks.
func (w *ClaudeCodeHookWriter) addBackendHooks(settings *claudeCodeSettings, backendHooks config.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(settings, eventName, h)
		}
	}
}

// addHook adds a single hook to the settings for the given event.
func (w *ClaudeCodeHookWriter) addHook(settings *claudeCodeSettings, eventName string, h config.Hook) {
	ccHook := claudeCodeHook{
		Type:    h.Type,
		Command: h.Command,
		Prompt:  h.Prompt,
		Timeout: h.Timeout,
		Async:   h.Async,
		SCM:     computeHookHash(h),
	}

	// Default type to "command"
	if ccHook.Type == "" {
		ccHook.Type = "command"
	}

	// Find or create matcher entry
	matcher := h.Matcher
	matchers := settings.Hooks[eventName]

	// Look for existing matcher with same pattern
	found := false
	for i, m := range matchers {
		if m.Matcher == matcher {
			matchers[i].Hooks = append(matchers[i].Hooks, ccHook)
			found = true
			break
		}
	}

	if !found {
		matchers = append(matchers, claudeCodeHookMatcher{
			Matcher: matcher,
			Hooks:   []claudeCodeHook{ccHook},
		})
	}

	settings.Hooks[eventName] = matchers
}

// AppMCPServerName is the name used for the ctxloom MCP server in settings.
const AppMCPServerName = "ctxloom"

// addMCPServersToConfig adds MCP servers from config to .mcp.json config.
func (w *ClaudeCodeHookWriter) addMCPServersToConfig(mcpConfig *claudeCodeMCPConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer) {
	if mcpConfig.MCPServers == nil {
		mcpConfig.MCPServers = make(map[string]claudeCodeMCPServer)
	}

	// Auto-register ctxloom's own MCP server unless disabled
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		mcpConfig.MCPServers[AppMCPServerName] = claudeCodeMCPServer{
			Command: ctxloomBinary,
			Args:    ctxloomMCPArgs,
			Cwd:     "${CLAUDE_PROJECT_DIR}", // Run in project directory so findAppDir works
			SCM:     "ctxloom-auto",          // Marker for auto-registered ctxloom server
		}
	}

	// Add MCP servers from profile bundles (loaded first, can be overridden)
	for name, server := range bundleMCP {
		mcpConfig.MCPServers[name] = claudeCodeMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     server.SCM, // Already marked ctxloom with bundle source
		}
	}

	if mcp == nil {
		return
	}

	// Add unified MCP servers (overrides bundle servers if same name)
	for name, server := range mcp.Servers {
		mcpConfig.MCPServers[name] = claudeCodeMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     computeMCPServerHash(server), // Marker for ctxloom-managed
		}
	}

	// Add backend-specific MCP servers (passthrough)
	if backendServers, ok := mcp.Plugins["claude-code"]; ok {
		for name, server := range backendServers {
			mcpConfig.MCPServers[name] = claudeCodeMCPServer{
				Command: server.Command,
				Args:    server.Args,
				SCM:     computeMCPServerHash(server),
			}
		}
	}
}

// computeMCPServerHash computes a hash from the MCP server's defining fields.
func computeMCPServerHash(s config.MCPServer) string {
	parts := []string{s.Command}
	parts = append(parts, s.Args...)
	data := strings.Join(parts, "|")
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8])
}

// GeminiHookWriter writes hooks to Gemini CLI's settings.json format.
type GeminiHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
}

// getFS returns the filesystem to use, defaulting to the OS filesystem.
func (w *GeminiHookWriter) getFS() afero.Fs {
	return getFS(w.FS)
}

// HooksPath returns the path to Gemini's project-level settings.json file.
func (w *GeminiHookWriter) HooksPath(projectDir string) string {
	return filepath.Join(projectDir, ".gemini", "settings.json")
}

// geminiSettings represents the structure of .gemini/settings.json
type geminiSettings struct {
	Hooks      map[string][]geminiHookGroup `json:"hooks,omitempty"`
	MCPServers map[string]geminiMCPServer   `json:"mcpServers,omitempty"`
	// Preserve other settings
	Other map[string]json.RawMessage `json:"-"`
}

// geminiHookGroup is one entry in a Gemini hook event array: a matcher plus the
// command hooks that fire for it. This nested shape (event → group → hooks[]) is
// Gemini's required schema — a flat {"command": …} object (Claude's shape) is
// silently ignored by Gemini, so ctxloom hooks must be emitted this way to fire.
type geminiHookGroup struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []geminiHookEntry `json:"hooks"`
}

// geminiHookEntry is a single command hook. Gemini requires type:"command" and
// expects timeout in milliseconds (Claude uses seconds). name is a durable,
// Gemini-serialized field used to mark and later identify ctxloom-managed hooks
// for clean removal — more robust than command-substring matching.
type geminiHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// geminiCtxloomHookName marks a hook entry as ctxloom-managed in the durable
// (serialized) name field.
const geminiCtxloomHookName = "ctxloom-managed"

// geminiMCPServer represents an MCP server in Gemini CLI format.
type geminiMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	SCM     string   `json:"-"` // Internal marker for ctxloom-managed servers (not serialized - Gemini CLI rejects unknown fields)
}

// SettingsPath returns the path to Gemini's settings.json file.
func (w *GeminiHookWriter) SettingsPath(projectDir string) string {
	return w.HooksPath(projectDir)
}

// WriteSettings implements SettingsWriter for Gemini CLI.
func (w *GeminiHookWriter) WriteSettings(hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &config.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)

	// Ensure .gemini directory exists
	geminiDir := filepath.Dir(settingsPath)
	if err := fs.MkdirAll(geminiDir, 0755); err != nil {
		return fmt.Errorf("failed to create .gemini directory: %w", err)
	}

	// Load existing settings
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing settings: %w", err)
	}

	// Remove old ctxloom-managed hooks from settings
	w.removeCtxloomHooks(settings)

	// Remove old ctxloom-managed MCP servers
	w.removeCtxloomMCPServers(settings)

	// Add ctxloom hooks from unified config
	w.addUnifiedHooks(settings, hooks.Unified)

	// Add ctxloom hooks from backend-specific passthrough
	if backendHooks, ok := hooks.Plugins["gemini"]; ok {
		w.addBackendHooks(settings, backendHooks)
	}

	// Add MCP servers from config and bundles
	w.addMCPServers(settings, mcp, bundleMCP)

	// Write settings back
	return w.saveSettings(settingsPath, settings)
}

// WriteHooks implements HookWriter for Gemini CLI (backwards compatible).
func (w *GeminiHookWriter) WriteHooks(cfg *config.HooksConfig, projectDir string) error {
	return w.WriteSettings(cfg, nil, nil, projectDir)
}

// loadSettings loads existing settings.json or returns empty settings.
func (w *GeminiHookWriter) loadSettings(path string) (*geminiSettings, error) {
	settings := &geminiSettings{
		Hooks:      make(map[string][]geminiHookGroup),
		MCPServers: make(map[string]geminiMCPServer),
		Other:      make(map[string]json.RawMessage),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}

	// First unmarshal to get all fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse settings.json: %w", err)
	}

	// Extract hooks separately
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			return nil, fmt.Errorf("failed to parse hooks: %w", err)
		}
		delete(raw, "hooks")
	}

	// Extract mcpServers separately
	if mcpRaw, ok := raw["mcpServers"]; ok {
		if err := json.Unmarshal(mcpRaw, &settings.MCPServers); err != nil {
			return nil, fmt.Errorf("failed to parse mcpServers: %w", err)
		}
		delete(raw, "mcpServers")
	}

	// Preserve other fields
	settings.Other = raw

	return settings, nil
}

// warn outputs a warning message to stderr.
func (w *GeminiHookWriter) warn(format string, args ...interface{}) {
	warn(format, args...)
}

// saveSettings writes settings back to settings.json.
func (w *GeminiHookWriter) saveSettings(path string, settings *geminiSettings) error {
	// Build output map starting with preserved fields
	output := make(map[string]interface{})
	for k, v := range settings.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			w.warn("failed to preserve setting %q: %v", k, err)
			continue // Skip corrupted field
		}
		output[k] = val
	}

	// Add hooks if non-empty
	if len(settings.Hooks) > 0 {
		output["hooks"] = settings.Hooks
	}

	// Add mcpServers if non-empty
	if len(settings.MCPServers) > 0 {
		output["mcpServers"] = settings.MCPServers
	}

	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	fs := w.getFS()
	return afero.WriteFile(fs, path, data, 0644)
}

// removeCtxloomHooks removes ctxloom-managed hooks from settings, descending
// into Gemini's nested group→hooks[] shape. A hook entry is ctxloom-managed if
// its durable name marker matches, or (defensively, for entries written before
// the marker existed or by an external flat writer) its command's executable
// token is ctxloom. Groups left with no entries are dropped, and events left
// with no groups are removed.
func (w *GeminiHookWriter) removeCtxloomHooks(settings *geminiSettings) {
	for eventName, groups := range settings.Hooks {
		var keptGroups []geminiHookGroup
		for _, g := range groups {
			var keptEntries []geminiHookEntry
			for _, e := range g.Hooks {
				if e.Name == geminiCtxloomHookName || isCtxloomManaged(e.Command) {
					continue // ctxloom-managed — drop
				}
				keptEntries = append(keptEntries, e)
			}
			if len(keptEntries) > 0 {
				g.Hooks = keptEntries
				keptGroups = append(keptGroups, g)
			}
		}
		if len(keptGroups) > 0 {
			settings.Hooks[eventName] = keptGroups
		} else {
			delete(settings.Hooks, eventName)
		}
	}
}

// ctxloomBinary is the bare executable name written into hook commands,
// the statusLine, and the auto-registered MCP server entry. We
// deliberately do NOT bake an absolute path into these files: a path
// goes stale the moment the binary moves (the `/usr/bin/ctxloom`
// regression), whereas a bare name re-resolves against PATH every time
// the command fires. The currently-running binary's path is still
// available in-process via GetExecutablePath — used by WarnOnCtxloomPathSkew
// to flag the one case bare can't handle (a different ctxloom earlier on
// PATH), and by relaunch code that must re-exec itself.
const ctxloomBinary = "ctxloom"

// ctxloomMCPArgs is the arg list passed to the ctxloom binary when it
// is auto-registered as an MCP server.
var ctxloomMCPArgs = []string{"mcp"}

// isCtxloomManaged reports whether a command was installed by ctxloom. We
// treat ANY command whose executable token is `ctxloom` (bare, absolute
// path, or quoted) as ctxloom-managed. It drives recognition across every
// slot ctxloom writes — hooks, the statusLine, and the auto-registered MCP
// server. Examples:
//
//	ctxloom hook inject-context …
//	ctxloom hook stamp-plan
//	"/usr/bin/ctxloom" hook hud
//	/home/me/go/bin/ctxloom hook session-bind
//
// Without this broad recognition, cleanup only catches the entry bearing
// the current marker: bundle-shipped hooks accumulate duplicates on every
// apply-hooks run, and a statusLine/MCP entry whose verb or marker drifted
// (e.g. the legacy `ctxloom hook hud`) is mistaken for user-authored and
// orphaned.
func isCtxloomManaged(command string) bool {
	return agent.IsManaged(command, "ctxloom")
}

// addUnifiedHooks translates unified hooks to Gemini CLI format and adds them.
func (w *GeminiHookWriter) addUnifiedHooks(settings *geminiSettings, unified config.UnifiedHooks) {
	// SessionStart -> SessionStart
	for _, h := range unified.SessionStart {
		w.addHook(settings, "SessionStart", h)
	}

	// SessionEnd -> SessionEnd
	for _, h := range unified.SessionEnd {
		w.addHook(settings, "SessionEnd", h)
	}

	// PreTool -> BeforeTool
	for _, h := range unified.PreTool {
		w.addHook(settings, "BeforeTool", h)
	}

	// PostTool -> AfterTool
	for _, h := range unified.PostTool {
		w.addHook(settings, "AfterTool", h)
	}
}

// addBackendHooks adds backend-specific passthrough hooks.
func (w *GeminiHookWriter) addBackendHooks(settings *geminiSettings, backendHooks config.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(settings, eventName, h)
		}
	}
}

// addHook adds a single hook to the settings for the given event, emitting
// Gemini's nested group→hooks[] shape with type:"command", the ctxloom name
// marker, and the timeout converted from seconds to Gemini's milliseconds.
func (w *GeminiHookWriter) addHook(settings *geminiSettings, eventName string, h config.Hook) {
	entry := geminiHookEntry{
		Type:    "command",
		Command: h.Command,
		Name:    geminiCtxloomHookName,
		Timeout: hookTimeoutMillis(h.Timeout),
	}
	group := geminiHookGroup{Matcher: h.Matcher, Hooks: []geminiHookEntry{entry}}
	settings.Hooks[eventName] = append(settings.Hooks[eventName], group)
}

// hookTimeoutMillis converts a hook timeout from seconds (ctxloom/Claude's unit)
// to milliseconds (Gemini's unit). Zero or negative stays zero so Gemini applies
// its own default.
func hookTimeoutMillis(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	return seconds * 1000
}

// removeCtxloomMCPServers removes ctxloom-managed MCP servers from settings.
// Since _ctxloom is not serialized to JSON (Gemini CLI rejects unknown fields),
// we track ctxloom-managed servers by the well-known name "ctxloom".
func (w *GeminiHookWriter) removeCtxloomMCPServers(settings *geminiSettings) {
	// Remove the well-known ctxloom server name
	delete(settings.MCPServers, AppMCPServerName)
}

// addMCPServers adds MCP servers from config to settings.
func (w *GeminiHookWriter) addMCPServers(settings *geminiSettings, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer) {
	if settings.MCPServers == nil {
		settings.MCPServers = make(map[string]geminiMCPServer)
	}

	// Auto-register ctxloom's own MCP server unless disabled
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		settings.MCPServers[AppMCPServerName] = geminiMCPServer{
			Command: ctxloomBinary,
			Args:    ctxloomMCPArgs,
			SCM:     "ctxloom-auto",
		}
	}

	// Add MCP servers from profile bundles (loaded first, can be overridden)
	for name, server := range bundleMCP {
		settings.MCPServers[name] = geminiMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     server.SCM, // Already marked ctxloom with bundle source
		}
	}

	if mcp == nil {
		return
	}

	// Add unified MCP servers (overrides bundle servers if same name)
	for name, server := range mcp.Servers {
		settings.MCPServers[name] = geminiMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     computeMCPServerHash(server),
		}
	}

	// Add backend-specific MCP servers (passthrough)
	if backendServers, ok := mcp.Plugins["gemini"]; ok {
		for name, server := range backendServers {
			settings.MCPServers[name] = geminiMCPServer{
				Command: server.Command,
				Args:    server.Args,
				SCM:     computeMCPServerHash(server),
			}
		}
	}
}

// ContextInjectionTimeout is the timeout for the context injection hook in seconds.
const ContextInjectionTimeout = 60

// NewContextInjectionHook creates the SessionStart hook that injects
// assembled context into the agent. The Command is emitted as the bare
// name `ctxloom` (see ctxloomBinary) so it re-resolves via PATH at fire
// time and never goes stale when the binary moves.
//
// workDir is the project directory where the context file lives.
// Resolved to an absolute path because Claude Code can launch the
// hook from a different cwd.
func NewContextInjectionHook(hash, workDir string) config.Hook {
	return config.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s %s", shellSingleQuote(absOrSelf(workDir)), hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// NewContextInjectionChunkHook builds one of N ordered context-injection hooks.
// Each invocation emits a single sub-cap chunk (part k of total) and uses the
// flock rendezvous (AwaitTurn) to complete in order, so the harness — which
// injects parallel hook output in completion order — sees the chunks in
// sequence. See NewContextInjectionHooks for when chunking kicks in.
func NewContextInjectionChunkHook(hash, workDir string, part, total int) config.Hook {
	return config.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s --part %d --of %d %s",
			shellSingleQuote(absOrSelf(workDir)), part, total, hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// absOrSelf resolves workDir to an absolute path (Claude Code may launch the
// hook from a different cwd), falling back to the input on error.
func absOrSelf(workDir string) string {
	if abs, err := filepath.Abs(workDir); err == nil {
		return abs
	}
	return workDir
}

// NewContextInjectionHooks returns the SessionStart context-injection hook(s)
// for the given content hash. It reads the (content-addressed, immutable)
// context file to decide the split: content that fits in one sub-cap chunk —
// or a missing/unreadable file — yields a single legacy whole-content hook;
// larger content yields N ordered chunk hooks. Reading the file here and in the
// hook with the same ChunkContext guarantees write-time and run-time agree on
// N. Best-effort by design: any read error falls back to the single hook (the
// runtime hook then emits nothing if the file is truly empty).
func NewContextInjectionHooks(hash, workDir string) []config.Hook {
	content, _ := ReadContextFile(workDir, hash)
	chunks := ChunkContext(content)
	if len(chunks) <= 1 {
		return []config.Hook{NewContextInjectionHook(hash, workDir)}
	}
	hooks := make([]config.Hook, 0, len(chunks))
	for k := 1; k <= len(chunks); k++ {
		hooks = append(hooks, NewContextInjectionChunkHook(hash, workDir, k, len(chunks)))
	}
	return hooks
}

// AppendManagedDynamicHooks appends the ctxloom-managed hooks that are
// assembled dynamically (rather than read verbatim from one config block) onto
// unified: the bundle-shipped hooks (SCM-tagged — e.g. `session bind`,
// `stamp-plan`) and, when contextHash is non-empty, the
// SessionStart context-injection hook.
//
// Both writers of settings.json route through this: the `ctxloom run` Setup
// path (BaseLifecycle.MergeConfigHooks) and operations.ApplyHooks. WriteSettings
// reconciles by removing ALL ctxloom hooks and re-adding only the writer's
// assembled set, so any writer that assembled a partial set silently dropped
// the rest. That is exactly what broke forward-bind: Setup never resolved
// bundle hooks, so every `ctxloom run` session launched with a settings.json
// missing `session bind`; apply-hooks could likewise drop inject-context.
// Keeping the assembly in one place guarantees every writer produces an
// identical, complete managed set.
func AppendManagedDynamicHooks(unified *config.UnifiedHooks, cfg *config.Config, workDir, contextHash string) {
	if unified == nil || cfg == nil {
		return
	}
	unified.Append(cfg.ResolveBundleHooks())
	if contextHash != "" {
		unified.SessionStart = append(unified.SessionStart, NewContextInjectionHooks(contextHash, workDir)...)
	}
}

// AssembleManagedHooks builds the COMPLETE ctxloom-managed hook set that every
// writer of a backend settings file must produce identically: config-level
// hooks, default-profile-shipped hooks, bundle-shipped hooks, and (when
// contextHash is non-empty) the context-injection hook.
//
// Both writers route through this — the `ctxloom run` Setup path
// (BaseLifecycle.MergeConfigHooks) and operations.ApplyHooks. WriteSettings
// reconciles by removing ALL ctxloom hooks and re-adding only the writer's
// assembled set, so any divergence between the writers silently drops whatever
// one assembled but the other didn't. Setup used to merge default-profile
// hooks while apply-hooks did not, so a profile-shipped SessionStart hook would
// be written at Setup and dropped by the next apply-hooks reconcile — the same
// class of failure that broke forward-bind. Keeping the full assembly here
// guarantees both writers produce an identical, complete set.
//
// Returns a fresh HooksConfig each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
func AssembleManagedHooks(cfg *config.Config, workDir, contextHash string) *config.HooksConfig {
	hooks := &config.HooksConfig{Plugins: make(map[string]config.BackendHooks)}
	if cfg == nil {
		return hooks
	}
	// Config-level hooks.
	mergeHooksConfig(hooks, &cfg.Hooks)
	// Default-profile-shipped hooks.
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName)
		if err != nil {
			continue
		}
		mergeHooksConfig(hooks, &resolved.Hooks)
	}
	// Bundle-shipped hooks + the context-injection hook.
	AppendManagedDynamicHooks(&hooks.Unified, cfg, workDir, contextHash)
	return hooks
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a
// /bin/sh command string, escaping embedded single quotes as the standard
// '\” idiom. Unlike double-quoting, single quotes neutralize spaces, $,
// backticks, and backslashes — so a project path containing any of those
// can't break the command split or inject shell behavior.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mergeHooksConfig merges source hooks into dest hooks.
func mergeHooksConfig(dest *config.HooksConfig, src *config.HooksConfig) {
	if src == nil || dest == nil {
		return
	}

	// Merge unified hooks
	dest.Unified.PreTool = append(dest.Unified.PreTool, src.Unified.PreTool...)
	dest.Unified.PostTool = append(dest.Unified.PostTool, src.Unified.PostTool...)
	dest.Unified.SessionStart = append(dest.Unified.SessionStart, src.Unified.SessionStart...)
	dest.Unified.SessionEnd = append(dest.Unified.SessionEnd, src.Unified.SessionEnd...)
	dest.Unified.PreShell = append(dest.Unified.PreShell, src.Unified.PreShell...)
	dest.Unified.PostFileEdit = append(dest.Unified.PostFileEdit, src.Unified.PostFileEdit...)

	// Merge plugin-specific hooks
	if dest.Plugins == nil {
		dest.Plugins = make(map[string]config.BackendHooks)
	}
	for name, hooks := range src.Plugins {
		if dest.Plugins[name] == nil {
			dest.Plugins[name] = make(config.BackendHooks)
		}
		for event, eventHooks := range hooks {
			dest.Plugins[name][event] = append(dest.Plugins[name][event], eventHooks...)
		}
	}
}
