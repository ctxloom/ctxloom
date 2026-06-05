// Package gemini is ctxloom's Gemini CLI agent: the settings/hooks writer that
// implements agent.SettingsWriter. Moved from internal/lm/backends in P0 step
// 4d; the launch backend follows in 4e.
package gemini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/shared/wire"
)

// NewWriter constructs the Gemini CLI settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &GeminiHookWriter{FS: o.FS}
}

// Shims to the shared core helpers/symbols so the moved code below reads
// unchanged (ctxloomBinary/ctxloomMCPArgs/isCtxloomManaged come in with the
// moved block).
type SettingsStatus = agent.SettingsStatus

const AppMCPServerName = agent.MCPServerName

var (
	getFS           = agent.GetFS
	atomicWriteFile = agent.AtomicWriteFile
)

func warn(format string, args ...any)              { agent.Warn(format, args...) }
func computeHookHash(h wire.Hook) string           { return agent.ComputeHookHash(h) }
func computeMCPServerHash(s wire.MCPServer) string { return agent.ComputeMCPServerHash(s) }

// ----- moved verbatim from internal/lm/backends (hooks.go + uninstall.go) -----
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
func (w *GeminiHookWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
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
func (w *GeminiHookWriter) WriteHooks(cfg *wire.HooksConfig, projectDir string) error {
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
const ctxloomBinary = agent.CtxloomBinary

// ctxloomMCPArgs is the arg list passed to the ctxloom binary when it
// is auto-registered as an MCP server.
var ctxloomMCPArgs = agent.CtxloomMCPArgs

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
func (w *GeminiHookWriter) addUnifiedHooks(settings *geminiSettings, unified wire.UnifiedHooks) {
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
func (w *GeminiHookWriter) addBackendHooks(settings *geminiSettings, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(settings, eventName, h)
		}
	}
}

// addHook adds a single hook to the settings for the given event, emitting
// Gemini's nested group→hooks[] shape with type:"command", the ctxloom name
// marker, and the timeout converted from seconds to Gemini's milliseconds.
func (w *GeminiHookWriter) addHook(settings *geminiSettings, eventName string, h wire.Hook) {
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
func (w *GeminiHookWriter) addMCPServers(settings *geminiSettings, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) {
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

// RemoveSettings implements SettingsWriter for Gemini CLI: it clears
// ctxloom-managed hooks and MCP servers from settings.json, leaving an absent
// file absent.
func (w *GeminiHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return nil
	}
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing settings: %w", err)
	}
	w.removeCtxloomHooks(settings)
	w.removeCtxloomMCPServers(settings)
	return w.saveSettings(settingsPath, settings)
}

// Status implements SettingsWriter for Gemini CLI.
func (w *GeminiHookWriter) Status(projectDir string) (SettingsStatus, error) {
	fs := w.getFS()
	var status SettingsStatus
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return status, nil
	}
	status.SettingsExists = true
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return status, fmt.Errorf("failed to load existing settings: %w", err)
	}
	status.HooksPresent = geminiHasManagedHook(settings)
	for _, server := range settings.MCPServers {
		if server.SCM != "" || server.Command != "" && isCtxloomManaged(server.Command) {
			status.MCPPresent = true
			break
		}
	}
	return status, nil
}

// geminiHasManagedHook reports whether any configured hook is ctxloom-managed,
// descending Gemini's event→group→hooks[] shape.
func geminiHasManagedHook(settings *geminiSettings) bool {
	for _, groups := range settings.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Name == geminiCtxloomHookName || isCtxloomManaged(hook.Command) {
					return true
				}
			}
		}
	}
	return false
}
