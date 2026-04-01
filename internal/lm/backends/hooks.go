package backends

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/spf13/afero"
)

// SettingsWriter writes hooks and MCP servers to backend-specific configuration files.
type SettingsWriter interface {
	// WriteSettings writes hooks and MCP servers to the backend's config file.
	// It preserves user-defined settings and adds/updates ctxloom-managed ones.
	// bundleMCP contains MCP servers resolved from profile bundles.
	WriteSettings(hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string) error

	// WriteHooks writes hooks to the backend's config file (backwards compatible).
	WriteHooks(cfg *config.HooksConfig, projectDir string) error

	// SettingsPath returns the path to the settings configuration file.
	SettingsPath(projectDir string) string

	// HooksPath returns the path to the hooks configuration file (alias for SettingsPath).
	HooksPath(projectDir string) string
}

// HookWriter is kept for backwards compatibility.
type HookWriter = SettingsWriter

// settingsOptions holds configuration for settings operations.
type settingsOptions struct {
	fs afero.Fs
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

// WriteSettings writes hooks and MCP servers for the specified backend.
// If the backend doesn't support settings, this is a no-op.
// bundleMCP contains MCP servers resolved from profile bundles.
// Use WithSettingsFS to provide a custom filesystem for testing.
func WriteSettings(backendName string, hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string, opts ...SettingsOption) error {
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}

	writer := GetSettingsWriter(backendName, options.fs)
	if writer == nil {
		return nil // Backend doesn't support settings
	}
	return writer.WriteSettings(hooks, mcp, bundleMCP, projectDir)
}

// WriteHooks writes hooks for the specified backend (backwards compatible).
// settingsWriterRegistry maps backend names to their settings writer constructors.
var settingsWriterRegistry = map[string]func(afero.Fs) SettingsWriter{
	"claude-code": func(fs afero.Fs) SettingsWriter { return &ClaudeCodeHookWriter{FS: fs} },
	"gemini":      func(fs afero.Fs) SettingsWriter { return &GeminiHookWriter{FS: fs} },
}

// GetSettingsWriter returns a SettingsWriter for the named backend, or nil if not supported.
// If fs is provided, it will be used for filesystem operations; otherwise the OS filesystem is used.
func GetSettingsWriter(name string, fs afero.Fs) SettingsWriter {
	if constructor, ok := settingsWriterRegistry[name]; ok {
		return constructor(fs)
	}
	return nil
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

// claudeCodeSettings represents the structure of .claude/settings.json
// Note: MCP servers are now stored in .mcp.json, not here.
type claudeCodeSettings struct {
	Hooks map[string][]claudeCodeHookMatcher `json:"hooks,omitempty"`
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
	Cwd     string   `json:"cwd,omitempty"` // Working directory for the server
	SCM     string   `json:"_ctxloom,omitempty"` // Marker identifying ctxloom-managed servers
}

// claudeCodeHookMatcher represents a hook matcher entry in Claude Code format.
type claudeCodeHookMatcher struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []claudeCodeHook `json:"hooks"`
}

// claudeCodeHook represents a single hook in Claude Code format.
type claudeCodeHook struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Async   bool   `json:"async,omitempty"`
	SCM     string `json:"_ctxloom,omitempty"` // Hash identifying ctxloom-managed hooks
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

	// Note: mcpServers are NOT written here - they go to .mcp.json

	data, err := json.MarshalIndent(output, "", "  ")
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
	data, err := json.MarshalIndent(mcpConfig, "", "  ")
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

	// Remove old ctxloom-managed MCP servers
	for name, server := range mcpConfig.MCPServers {
		if server.SCM != "" {
			delete(mcpConfig.MCPServers, name)
		}
	}

	// Add MCP servers
	w.addMCPServersToConfig(mcpConfig, mcp, bundleMCP)

	// Write MCP config back
	return w.saveMCPConfig(mcpPath, mcpConfig)
}

// removeCtxloomHooks removes all hooks with _ctxloom field from settings.
func (w *ClaudeCodeHookWriter) removeCtxloomHooks(settings *claudeCodeSettings) {
	for eventName, matchers := range settings.Hooks {
		var filteredMatchers []claudeCodeHookMatcher
		for _, matcher := range matchers {
			var filteredHooks []claudeCodeHook
			for _, hook := range matcher.Hooks {
				if hook.SCM == "" {
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
			Command: GetCtxloomMCPCommand(),
			Args:    GetCtxloomMCPArgs(),
			Cwd:     "${CLAUDE_PROJECT_DIR}", // Run in project directory so findAppDir works
			SCM:     "ctxloom-auto",              // Marker for auto-registered ctxloom server
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
// GeminiHookWriter writes hooks to Gemini's settings.json format.
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
	Hooks      map[string][]geminiHook      `json:"hooks,omitempty"`
	MCPServers map[string]geminiMCPServer   `json:"mcpServers,omitempty"`
	// Preserve other settings
	Other map[string]json.RawMessage `json:"-"`
}

// geminiHook represents a single hook in Gemini CLI format.
type geminiHook struct {
	Command string `json:"command,omitempty"`
	SCM     string `json:"-"` // Internal marker for ctxloom-managed hooks (not serialized - Gemini CLI rejects unknown fields)
}

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
		Hooks:      make(map[string][]geminiHook),
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

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	fs := w.getFS()
	return afero.WriteFile(fs, path, data, 0644)
}

// removeCtxloomHooks removes ctxloom-managed hooks from settings.
// Since _ctxloom is not serialized to JSON (Gemini CLI rejects unknown fields),
// we identify ctxloom hooks by checking if the command contains "ctxloom" and "inject-context".
func (w *GeminiHookWriter) removeCtxloomHooks(settings *geminiSettings) {
	for eventName, hooks := range settings.Hooks {
		var filteredHooks []geminiHook
		for _, hook := range hooks {
			// Keep hooks that are NOT ctxloom-managed
			if !isCtxloomManagedHook(hook.Command) {
				filteredHooks = append(filteredHooks, hook)
			}
		}
		if len(filteredHooks) > 0 {
			settings.Hooks[eventName] = filteredHooks
		} else {
			delete(settings.Hooks, eventName)
		}
	}
}

// isCtxloomManagedHook returns true if the hook command appears to be ctxloom-managed.
func isCtxloomManagedHook(command string) bool {
	// ctxloom inject-context hooks contain both "ctxloom" and "inject-context"
	return strings.Contains(command, "ctxloom") && strings.Contains(command, "inject-context")
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

// addHook adds a single hook to the settings for the given event.
func (w *GeminiHookWriter) addHook(settings *geminiSettings, eventName string, h config.Hook) {
	hook := geminiHook{
		Command: h.Command,
		SCM:     computeHookHash(h),
	}

	settings.Hooks[eventName] = append(settings.Hooks[eventName], hook)
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
			Command: GetCtxloomMCPCommand(),
			Args:    GetCtxloomMCPArgs(),
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

// NewContextInjectionHook creates a hook for context injection using the symlinked ctxloom binary.
// hash is the context file hash to pass to the inject-context command.
// workDir is the project directory where the context file lives.
func NewContextInjectionHook(hash, workDir string) config.Hook {
	return config.Hook{
		Command: GetContextInjectionCommand(hash, workDir),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
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

// MergeMCPConfig merges source MCP config into dest.
// Later sources override earlier ones for the same server name.
// MergeMCPConfig merges src MCP config into dest.
// Deprecated: Use config.MergeMCPConfig directly.
func MergeMCPConfig(dest *config.MCPConfig, src *config.MCPConfig) {
	config.MergeMCPConfig(dest, src)
}

