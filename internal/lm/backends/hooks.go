package backends

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/agent/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/spf13/afero"
)

// SettingsWriter is the agent settings-writing contract. It lives in
// internal/agent (the engine-agnostic core) so a consumer can take the settings
// facet without the launch facet (Backend); aliased here for existing sites.
type SettingsWriter = agent.SettingsWriter

// HookWriter is kept for backwards compatibility.
type HookWriter = SettingsWriter

// Settings options + shared write helpers live in internal/agent (the
// engine-agnostic core) so the per-agent writers can use them without importing
// backends. settingsOptions is the unexported alias the local registry keeps
// using; SettingsOption and the With* funcs are re-exported for external callers
// (internal/operations).
type settingsOptions = agent.SettingsOptions

type SettingsOption = agent.SettingsOption

var (
	WithSettingsFS         = agent.WithSettingsFS
	WithStatusLineDisabled = agent.WithStatusLineDisabled
)

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
		return claude.NewWriter(*o)
	},
	"gemini": func(o *settingsOptions) SettingsWriter { return &GeminiHookWriter{FS: o.FS} },
}

// AppMCPServerName is the name used for the ctxloom MCP server in settings
// (shared with the claude writer, now in internal/agent/claude).
const AppMCPServerName = agent.MCPServerName

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
	return newSettingsWriter(name, &settingsOptions{FS: fs})
}

// BackendsWithSettings returns the names of all backends that support settings.
func BackendsWithSettings() []string {
	names := make([]string, 0, len(settingsWriterRegistry))
	for name := range settingsWriterRegistry {
		names = append(names, name)
	}
	return names
}

// computeHookHash delegates to agent.ComputeHookHash (transitional wrapper —
// removed when the writers move to the per-agent packages).
func computeHookHash(h config.Hook) string { return agent.ComputeHookHash(h) }

// =============================================================================
// Shared Helper Functions
// =============================================================================
// These helpers reduce code duplication between ClaudeCodeHookWriter and
// GeminiHookWriter implementations.

func getFS(fs afero.Fs) afero.Fs { return agent.GetFS(fs) }

func warn(format string, args ...any) { agent.Warn(format, args...) }

func atomicWriteFile(fs afero.Fs, path string, data []byte, desc string) error {
	return agent.AtomicWriteFile(fs, path, data, desc)
}

// computeMCPServerHash delegates to agent.ComputeMCPServerHash (shared by the
// claude + gemini writers).
func computeMCPServerHash(s config.MCPServer) string { return agent.ComputeMCPServerHash(s) }

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
