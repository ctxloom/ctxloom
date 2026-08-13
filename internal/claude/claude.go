// Package claude is ctxloom's Claude Code agent: the settings/hooks writer that
// implements agent.SettingsWriter. Moved from internal/lm/backends in P0 step
// 4c; the launch backend follows in 4e.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// NewWriter constructs the Claude Code settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &ClaudeCodeHookWriter{FS: o.FS}
}

// ----- moved verbatim from internal/lm/backends (hooks.go + uninstall.go) -----
// ClaudeCodeHookWriter writes hooks to Claude Code's settings.json format.
type ClaudeCodeHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// statusLineDisabled opts out of managing the ctxloom HUD statusline.
	statusLineDisabled bool
	// mcpCommandOverride, when non-empty, replaces agent.CtxloomCommand() as
	// the ctxloom-managed .mcp.json entry's command (see
	// agent.ResolveMCPCommand) — set ONLY for an isolated-container cell (the
	// dire-five fix). Empty (the default) preserves the host self-exec-
	// absolute behavior exactly.
	mcpCommandOverride string
}

// getFS returns the filesystem to use, defaulting to the OS filesystem. It is
// a spelling shortener for the 12 in-package call sites, not an injection
// seam — the seam is the FS field itself.
func (w *ClaudeCodeHookWriter) getFS() afero.Fs {
	return agent.GetFS(w.FS)
}

// ProjectSettingsPath returns the project-scoped Claude Code settings.json
// path (.claude/settings.json under projectDir). Exported for companion tools
// (ltk) that manage hooks in the same file, so the path convention has a
// single source of truth.
func ProjectSettingsPath(projectDir string) string {
	return filepath.Join(projectDir, ConfigDirName, SettingsFileName)
}

// GlobalSettingsPath returns the user-global Claude Code settings.json path
// (~/.claude/settings.json).
func GlobalSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName, SettingsFileName), nil
}

// GlobalCommandsDir returns the user-global Claude Code slash-command directory
// (~/.claude/commands). Claude Code loads this alongside the project-scoped
// <workdir>/.claude/commands, so a project copy byte-identical to a global one
// surfaces as a duplicate slash-command; the command writer dedups against this
// dir (see agent.WriteManagedCommandFiles).
func GlobalCommandsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName, CommandsDirName), nil
}

// SettingsPath returns the path to Claude Code's settings.json file.
func (w *ClaudeCodeHookWriter) SettingsPath(projectDir string) string {
	return ProjectSettingsPath(projectDir)
}

// MCPConfigPath returns the path to Claude Code's .mcp.json file.
// Note: MCP servers must be in .mcp.json (not settings.json) for ${CLAUDE_PROJECT_DIR}
// variable expansion to work. See: https://github.com/anthropics/claude-code/issues/4276
func (w *ClaudeCodeHookWriter) MCPConfigPath(projectDir string) string {
	return filepath.Join(projectDir, MCPFileName)
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
	Hooks       map[string][]claudeCodeHookMatcher `json:"hooks,omitempty"`
	StatusLine  *claudeCodeStatusLine              `json:"statusLine,omitempty"`
	Permissions *claudeCodePermissions             `json:"permissions,omitempty"`
	// Preserve other settings (including legacy mcpServers for backwards compat)
	Other map[string]json.RawMessage `json:"-"`
}

// claudeCodePermissions represents the "permissions" block of settings.json.
// ctxloom manages only Deny — the per-tool deny list (deny-tools.md's
// root-cause fix: denying claude-code's built-in Task tool forces delegation
// through ctxloom's own agent_run path, which resolves child profiles
// correctly, instead of Task's in-process sub-agent that inherits the
// coordinator's system prompt). Other preserves every other permissions key
// (allow, ask, defaultMode, additionalDirectories, …) verbatim — the same
// raw-passthrough idiom claudeCodeSettings.Other uses for unrelated top-level
// keys, so a user's own allow/ask rules round-trip untouched.
type claudeCodePermissions struct {
	Deny  []string                   `json:"deny,omitempty"`
	Other map[string]json.RawMessage `json:"-"`
}

// claudeCodeMCPConfig represents the structure of .mcp.json
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
type claudeCodeMCPConfig struct {
	MCPServers map[string]claudeCodeMCPServer `json:"mcpServers,omitempty"`
}

// claudeCodeMCPServer represents an MCP server configuration in Claude Code format.
type claudeCodeMCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`      // Environment variables for the server
	Cwd     string            `json:"cwd,omitempty"`      // Working directory for the server
	SCM     string            `json:"_ctxloom,omitempty"` // Marker identifying ctxloom-managed servers
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
func (w *ClaudeCodeHookWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	// Write hooks + statusline to settings.json. This legacy interface method
	// carries no deny_tools payload (the agent.SettingsWriter interface is
	// shared across every backend, so extending its signature is a
	// cross-module change out of scope here) — real launches deliver
	// deny_tools through the surfaces × cells seam (surfacedelivery.go's
	// DeliverSettings) instead, which this method's only live callers
	// (opencode's own writer, conformance tests) never route through.
	if err := w.writeSettingsFile(hooks, nil, projectDir); err != nil {
		return err
	}

	// Write MCP servers to .mcp.json (separate file where variable expansion works)
	return w.writeMCPConfig(projectDir, mcp, bundleMCP)
}

// writeSettingsFile writes the settings.json half of WriteSettings: it replaces
// ctxloom-managed hooks, (re)configures the managed statusline, and unions
// denyTools into permissions.deny under projectDir, preserving user-authored
// entries. It is factored out of WriteSettings — same bytes, same effects —
// so the delivery seam can materialize the settings surface (hooks +
// statusline + deny_tools) independently of the .mcp.json surface
// (writeMCPConfig); WriteSettings composes the two (with denyTools nil — see
// its doc).
func (w *ClaudeCodeHookWriter) writeSettingsFile(hooks *wire.HooksConfig, denyTools []string, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)

	// The lock spans the whole load-modify-save-and-ledger-write cycle below:
	// a SessionStart hook, the MCP server, the CLI, the runner, and an
	// in-container ctxloom (same file bind-mounted) all reach this same
	// settings.json unlocked otherwise — see agent.WithFileLock's doc.
	return agent.WithFileLock(fs, settingsPath, func() error {
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

		// Remove the hooks ctxloom wrote last time. Ownership comes from the shared
		// managed-content ledger beside settings.json — Claude Code's strict schema
		// forbids an in-file marker (claudeCodeHook.SCM is json:"-" and never
		// reaches disk), which is exactly what a SIDECAR record is for.
		led := ledger.Ledger{FS: fs, Dir: claudeDir, Warn: agent.Warn}
		owned, err := led.Read(ledger.SurfaceHooks)
		if err != nil {
			return err
		}
		w.removeCtxloomHooks(settings, owned)

		// Add ctxloom hooks from unified config
		w.addUnifiedHooks(settings, hooks.Unified)

		// Add ctxloom hooks from backend-specific passthrough
		if backendHooks, ok := hooks.Plugins["claude-code"]; ok {
			w.addBackendHooks(settings, backendHooks)
		}

		// Configure statusLine if not already set by the user.
		prevStatus, err := led.Read(ledger.SurfaceStatusLine)
		if err != nil {
			return err
		}
		statusOwned := settings.StatusLine != nil && len(prevStatus) > 0 &&
			prevStatus[0] == agent.ComputeCommandDigest(settings.StatusLine.Command)
		w.ensureStatusLine(settings, statusOwned)

		// Reconcile ctxloom's deny entries, withdrawing any it no longer declares.
		prevDeny, err := led.Read(ledger.SurfacePermissions)
		if err != nil {
			return err
		}
		nowDeny := w.mergeDenyTools(settings, denyTools, prevDeny)

		// Write hooks to settings.json, then record exactly what was written so the
		// next reconcile removes THOSE and nothing else.
		if err := w.saveSettings(settingsPath, settings); err != nil {
			return err
		}
		if err := led.Write(ledger.SurfaceHooks, w.managedHookDigests(settings)); err != nil {
			return err
		}
		if err := led.Write(ledger.SurfacePermissions, nowDeny); err != nil {
			return err
		}
		// Everything ctxloom wrote is now recorded, and only what it still writes:
		// the record is current state, never an append-only history, so it cannot
		// accumulate cruft — a surface ctxloom stops writing is cleared, not grown.
		var statusNow []string
		if settings.StatusLine != nil && agent.IsManaged(settings.StatusLine.Command, "ctxloom") {
			statusNow = []string{agent.ComputeCommandDigest(settings.StatusLine.Command)}
		}
		return led.Write(ledger.SurfaceStatusLine, statusNow)
	})
}

// WriteContext implements agent.ContextWriter for Claude Code: it merges the
// assembled context (req.Context) into the ctxloom-managed section of
// <projectDir>/CLAUDE.md, preserving any hand-authored content outside the
// markers BYTE-FOR-BYTE (taskloom lanky-plop — this used to be a bare
// whole-file afero.WriteFile with no read-first and no merge, which silently
// destroyed a team's hand-written CLAUDE.md). Empty content removes the managed
// section (and the file, when it was wholly ctxloom's). This is the STATIC
// context surface an externally-launched Claude Code session reads directly —
// the same payload the SessionStart injection hook delivers at runtime, but
// written to disk with ctxloom out of the loop. (The framed-cache /
// --append-system-prompt-file runtime path is separate: it writes its own
// out-of-cwd <hash>.sysprompt.md from the context string directly and never
// reads CLAUDE.md, so it is unaffected by this change.)
//
// The marker merge itself is the shared core (agent.WriteManagedContext),
// ported from antigravity's original .agents/AGENTS.md implementation so every
// backend that owns a human-editable context file shares one merge.
func (w *ClaudeCodeHookWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	path := filepath.Join(req.ProjectDir, ContextFileName)
	return agent.WriteManagedContext(w.getFS(), path, ContextFileName, req.Context, ContextFileName)
}

// loadSettings loads existing settings.json or returns empty settings for a
// missing file.
//
// On a PARSE failure it does NOT fabricate an empty settings object: the
// caller (writeSettingsFile) persists whatever loadSettings returns, so
// returning empty-but-valid settings here used to make ctxloom overwrite a
// user's corrupt-but-recoverable settings.json (permissions, env, hooks) with
// an empty one — silent data loss (taskloom lone-taste). Instead, on a parse
// failure the raw bytes are backed up to <path>.corrupt-<unix-timestamp> and
// a real error is returned so writeSettingsFile aborts before touching the
// file, pointing the user at the backup to fix by hand.
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
		return nil, w.corruptSettings(path, data, "settings.json (schema may have changed)", err, "to avoid overwriting it")
	}

	// Extract hooks separately
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			return nil, w.corruptSettings(path, data, "hooks", err, "to avoid dropping existing hooks")
		}
		delete(raw, "hooks")
	}

	// Extract statusLine separately. Unreadable is refused, not degraded, for
	// the same reason as hooks and permissions below: the delete runs
	// unconditionally and saveSettings re-emits the statusLine only from the
	// typed field, so "the user has none this code can recognize" ended as the
	// user's own statusLine being replaced by the managed HUD — or, with the
	// HUD opted out, deleted from the file outright.
	if slRaw, ok := raw["statusLine"]; ok {
		sl, err := w.parseStatusLine(path, data, slRaw)
		if err != nil {
			return nil, err
		}
		settings.StatusLine = sl
		delete(raw, "statusLine")
	}

	// Extract permissions separately: Deny is the ctxloom-managed sub-field,
	// unmarshaled into its typed slice; every sibling key (allow, ask,
	// defaultMode, …) is kept verbatim in Other so a user's own rules
	// round-trip untouched (see claudeCodePermissions's doc).
	//
	// A permissions block this code cannot read is treated exactly like
	// unparseable hooks, and for the same reason: the delete below used to
	// run unconditionally, and saveSettings only re-emits permissions when
	// the typed field is non-nil, so a warning was followed by the user's
	// allow/ask/defaultMode/additionalDirectories rules being dropped from
	// the file — silently, with no .corrupt backup, on a SECURITY surface.
	if permRaw, ok := raw["permissions"]; ok {
		perm, err := w.parsePermissions(path, data, permRaw)
		if err != nil {
			return nil, err
		}
		settings.Permissions = perm
		delete(raw, "permissions")
	}

	// A legacy mcpServers block stays exactly where it is, in Other. This
	// used to be deleted under a comment claiming a migration to .mcp.json —
	// but no migration code exists, nothing ever reads the block, and
	// writeMCPConfig only ever reads and writes .mcp.json. So the delete was
	// pure loss, and it ran on the UNINSTALL path too (removeSettingsFile →
	// loadSettings → saveSettings), meaning ctxloom destroyed a user's
	// servers while being removed.

	// Preserve other fields
	settings.Other = raw

	return settings, nil
}

// parseStatusLine decodes the statusLine block, refusing the whole read when
// it cannot. A statusLine is a single slot, but "ctxloom does not recognize it"
// is not the same as "the user has none": treating the two alike handed
// ensureStatusLine an empty slot to fill, which overwrote a value only the user
// had authored — and deleted it when the managed HUD is opted out. ctxloom is
// the wrong party to decide the fate of a value it just failed to read, so this
// takes the same stance as parsePermissions and the hooks block: back the
// original up and abort before anything is written.
func (w *ClaudeCodeHookWriter) parseStatusLine(path string, data []byte, raw json.RawMessage) (*claudeCodeStatusLine, error) {
	var sl claudeCodeStatusLine
	if err := json.Unmarshal(raw, &sl); err != nil {
		return nil, w.corruptSettings(path, data, "statusLine", err, "to avoid replacing or deleting the statusline already in it")
	}
	return &sl, nil
}

// parsePermissions splits the permissions block into the ctxloom-managed Deny
// list and the verbatim siblings (allow, ask, defaultMode, …) that must
// round-trip untouched. Anything it cannot read is refused, not dropped: see
// loadSettings' own comment for the data loss that caused.
func (w *ClaudeCodeHookWriter) parsePermissions(path string, data []byte, raw json.RawMessage) (*claudeCodePermissions, error) {
	var permMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &permMap); err != nil {
		return nil, w.corruptSettings(path, data, "permissions", err, "to avoid dropping existing permission rules")
	}
	perm := &claudeCodePermissions{}
	if denyRaw, ok := permMap["deny"]; ok {
		var deny []string
		if err := json.Unmarshal(denyRaw, &deny); err != nil {
			return nil, w.corruptSettings(path, data, "permissions.deny", err, "to avoid dropping existing permission rules")
		}
		perm.Deny = deny
		delete(permMap, "deny")
	}
	perm.Other = permMap
	return perm, nil
}

// corruptSettings is this writer's binding of agent.RefuseCorrupt (see its
// doc): back the original bytes up, then return an error so the caller aborts
// before touching the file. Every partial-parse failure in loadSettings and
// loadMCPConfig routes through here precisely so no future field can be added
// with a warn-and-continue branch — a warning is not a guard, and each of the
// paths that had one (permissions, permissions.deny, .mcp.json) was
// destroying user data behind it.
func (w *ClaudeCodeHookWriter) corruptSettings(path string, data []byte, what string, cause error, consequence string) error {
	return agent.RefuseCorrupt(w.getFS(), path, data, what, cause, consequence)
}

// permissionsOutput renders the permissions block for re-emission: the
// preserved sibling keys first, then the ctxloom-managed Deny list layered on
// top — the same shape as saveSettings' top-level output map. A sibling that
// cannot be re-encoded refuses the whole write (preserveFailure); dropping one
// silently would delete the user's own allow/ask/defaultMode rules.
func permissionsOutput(path string, perm *claudeCodePermissions) (map[string]interface{}, error) {
	out := make(map[string]interface{})
	for k, v := range perm.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			return nil, preserveFailure(path, "permissions."+k, err)
		}
		// The decode above is the GATE, not the value: what gets re-emitted is
		// the user's original bytes. See saveSettings for why.
		out[k] = json.RawMessage(v)
	}
	if len(perm.Deny) > 0 {
		out["deny"] = perm.Deny
	}
	return out, nil
}

// preserveFailure refuses a write that could not carry a preserved field
// through. Every key in settings.json is user-authored unless ctxloom manages
// it by name, so emitting the document without one is silent data loss on the
// user's own file — and for a permissions.* sibling, silent data loss on a
// security surface. Returning here leaves the file exactly as it was, the same
// stance loadSettings takes on a block it cannot parse.
//
// The bytes always ARE valid JSON (they were lifted out of a document that
// parsed), but valid JSON is not always decodable into `any`: a number outside
// float64's range is the reachable case, and the value has to survive it.
func preserveFailure(path, key string, cause error) error {
	return fmt.Errorf("refusing to write %s: cannot re-encode the existing %q setting: %w "+
		"(it would be dropped from the file; edit that value by hand to a form ctxloom can preserve)",
		path, key, cause)
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
			return preserveFailure(path, k, err)
		}
		// A preserved key is re-emitted as its ORIGINAL bytes, and the decode
		// above serves only as the gate that rejects what cannot be carried
		// through (preserveFailure). Handing the DECODED value to the
		// canonicaliser instead would round every number the user wrote past
		// float64's exact range — 1234567890123456789 comes back
		// 1234567890123456800 — a rewrite of the user's own file that no
		// warning or exit code reports.
		output[k] = json.RawMessage(v)
	}

	// Add hooks if non-empty
	if len(settings.Hooks) > 0 {
		output["hooks"] = settings.Hooks
	}

	// Add statusLine if configured
	if settings.StatusLine != nil {
		output["statusLine"] = settings.StatusLine
	}

	// Add permissions if configured: Other's preserved sibling keys first, then
	// the typed Deny list layered on top — same shape as the top-level output
	// map above (preserved fields, then ctxloom-managed ones).
	if settings.Permissions != nil {
		permOut, err := permissionsOutput(path, settings.Permissions)
		if err != nil {
			return err
		}
		if len(permOut) > 0 {
			output["permissions"] = permOut
		}
	}

	// Note: ctxloom's own MCP servers are NOT written here — they go to
	// .mcp.json. A LEGACY mcpServers block that was already in the user's
	// settings.json rides along in Other and is emitted verbatim above:
	// nothing migrates it, so dropping it would just delete it.

	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	return agent.AtomicWriteFile(w.getFS(), path, data, "settings")
}

// loadMCPConfig loads existing .mcp.json, or an empty config for a missing
// file — which is the only case that legitimately means "no servers yet".
//
// It is deliberately NOT fault-tolerant on a parse failure: its result is
// written straight back by writeMCPConfig, so "tolerating" an unreadable file
// meant deleting every server in it. See corruptSettings.
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
		// Returning the empty config here handed writeMCPConfig a blank slate
		// it then filled with ctxloom's servers and SAVED — deleting every
		// server the user had. The old warning conceded as much ("existing
		// MCP servers may not be preserved"); a warning is not a guard. This
		// now mirrors loadSettings, which was hardened for exactly this.
		return nil, w.corruptSettings(path, data, ".mcp.json", err, "to avoid deleting the MCP servers already in it")
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

	return agent.AtomicWriteFile(w.getFS(), path, data, ".mcp.json")
}

// writeMCPConfig writes MCP servers to .mcp.json.
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
func (w *ClaudeCodeHookWriter) writeMCPConfig(projectDir string, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) error {
	mcpPath := w.MCPConfigPath(projectDir)

	// See writeSettingsFile: the lock spans load through save, the whole RMW
	// cycle a concurrent writer of the SAME .mcp.json could otherwise race.
	return agent.WithFileLock(w.getFS(), mcpPath, func() error {
		// Ensure the target directory exists, as writeSettingsFile does for
		// .claude/. .mcp.json sits directly in projectDir, so this writer used to
		// depend on someone else having created it — and for an out-of-cwd delivery
		// that someone was whichever surface happened to be delivered first. When
		// the context surface delivered nothing (no context configured), the
		// per-session scratch directory was never created and this write failed with
		// a bare ENOENT about a temp file.
		if err := w.getFS().MkdirAll(projectDir, 0o755); err != nil {
			return fmt.Errorf("failed to create MCP config directory: %w", err)
		}

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
			if server.SCM != "" || agent.IsManaged(server.Command, "ctxloom") {
				delete(mcpConfig.MCPServers, name)
			}
		}

		// Add MCP servers
		w.addMCPServersToConfig(mcpConfig, mcp, bundleMCP)

		// Write MCP config back
		return w.saveMCPConfig(mcpPath, mcpConfig)
	})
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
func (w *ClaudeCodeHookWriter) ensureStatusLine(settings *claudeCodeSettings, prevOwned bool) {
	// If a statusline is set and ctxloom did not write it, it is the user's:
	// leave it alone. Ownership is the ledger's record of what ctxloom last
	// installed — the user is entitled to point their OWN statusline at the
	// ctxloom binary ("ctxloom hook hud --theme mine"), and matching on the
	// executable token, as this used to, took those over silently.
	//
	// The exact-command fallback covers the one case the ledger cannot: a
	// statusline ctxloom installed BEFORE the ledger existed has no record, and
	// leaving it stranded would freeze a stale command in the status bar
	// forever. Reclaiming ctxloom's own canonical command is bounded; reclaiming
	// any command that mentions ctxloom is not.
	if settings.StatusLine != nil && !prevOwned && settings.StatusLine.Command != ctxloomStatusLineCommand() {
		return
	}

	// Opt-out: don't manage a statusline. Clear any previously ctxloom-managed
	// one so the user ends up with their own choice (or none).
	if w.statusLineDisabled {
		settings.StatusLine = nil
		return
	}

	// Set or update ctxloom-managed statusLine. Command names the self-exec
	// absolute path (agent.CtxloomCommand) — see its doc for why: a bare
	// `ctxloom` re-resolves via PATH at fire time, which can silently
	// diverge from the binary that materialized this file.
	settings.StatusLine = &claudeCodeStatusLine{
		Type:    "command",
		Command: ctxloomStatusLineCommand(),
	}
}

// ctxloomStatusLineCommand is the exact statusline command ctxloom installs —
// one definition, so the writer and the ownership check can never disagree.
func ctxloomStatusLineCommand() string {
	return agent.CtxloomCommand() + " hook hud"
}

// mergeDenyTools unions denyTools into settings.Permissions.Deny —
// MONOTONIC ADD ONLY: an entry is never removed by a later apply or by
// RemoveSettings/uninstall (removeSettingsFile does not touch Permissions at
// all).
//
// This is a deliberate asymmetry from the hooks/MCP-server reconcile pattern
// (removeCtxloomHooks / writeMCPConfig's SCM-marker prune), which is safe to
// remove-then-readd because each entry carries an ownership marker (SCM /
// the command string itself). A plain string in a JSON deny array carries no
// such marker — Claude Code's settings schema has no room to attach one (the
// same strict-schema constraint documented on claudeCodeHook.SCM) — so
// ctxloom cannot distinguish "a denial IT added" from "a denial the user
// hand-wrote" well enough to safely retract just its own. Erring toward
// "stays denied" is the SAFE direction for a denial (it can never silently
// re-enable a tool a human or a prior ctxloom apply restricted); erring
// toward "stays allowed" would not be. A user who wants a denial gone edits
// settings.json by hand.
// mergeDenyTools reconciles ctxloom's deny entries into permissions.deny and
// returns the set it now claims, for the caller to record in the ledger.
//
// It used to only ever APPEND, which made every entry a one-way leak: config
// stopped declaring a tool and the deny stayed in the user's settings forever,
// because nothing recorded that ctxloom had put it there rather than the user.
// It now RECONCILES like every other surface — the previously claimed set
// (prev, from the ledger) is withdrawn first, so an entry ctxloom no longer
// declares actually goes away.
//
// Retracting a denial is a real security change, which is why it is keyed on
// the ledger and nothing else: only entries ctxloom RECORDED writing are
// removed. A deny the user wrote by hand was never recorded and is never
// touched, even when it is byte-identical to one ctxloom also declares — in
// that case ctxloom does not claim it either (see the `existing[t]` skip), so
// the user's entry outlives ctxloom's interest in it.
func (w *ClaudeCodeHookWriter) mergeDenyTools(settings *claudeCodeSettings, denyTools []string, prev []string) []string {
	claimed := make(map[string]bool, len(prev))
	for _, p := range prev {
		claimed[p] = true
	}

	// Withdraw what ctxloom previously claimed, so an entry it no longer
	// declares actually goes away. Only CLAIMED entries are removed: an
	// identical deny the USER wrote was never recorded, so it is never touched
	// — which is what makes retraction safe to do at all.
	if settings.Permissions != nil && len(claimed) > 0 {
		kept := settings.Permissions.Deny[:0]
		for _, d := range settings.Permissions.Deny {
			if !claimed[d] {
				kept = append(kept, d)
			}
		}
		settings.Permissions.Deny = kept
	}

	if len(denyTools) == 0 {
		return nil
	}
	if settings.Permissions == nil {
		settings.Permissions = &claudeCodePermissions{}
	}
	existing := make(map[string]bool, len(settings.Permissions.Deny))
	for _, d := range settings.Permissions.Deny {
		existing[d] = true
	}
	var now []string
	for _, t := range denyTools {
		if t == "" {
			continue
		}
		// An entry the USER already has is theirs, not ours to claim: adding it
		// to the ledger would hand a later run the right to delete it.
		if existing[t] {
			continue
		}
		existing[t] = true
		settings.Permissions.Deny = append(settings.Permissions.Deny, t)
		now = append(now, t)
	}
	return now
}

// removeCtxloomHooks removes all ctxloom-managed hooks from settings, pruning
// any matcher it empties.
//
// Identity is the command's leading EXECUTABLE TOKEN resolving to `ctxloom`
// (agent.IsManaged) — path-, quote- and VERB-agnostic. Two consequences follow
// from that and neither is a substring match on the command line: every
// `ctxloom <anything>` hook is removed, not only the inject-context callback, so
// the callback's subcommand can move without orphaning old installs; and a
// command that merely mentions ctxloom somewhere in its arguments is another
// tool's hook and is left alone.
//
// The SCM field is checked for in-memory hooks but is not serialized to JSON
// (Claude Code uses strict schema validation that rejects unknown fields).
// ctxloomMachineCallbacks are ctxloom's OWN hook targets — the machine
// callbacks it installs, never something a user would author for their own
// purposes. They are removed regardless of the ledger, for one reason the
// ledger cannot cover: a settings.json written before the ledger existed has
// no record, and a stale `inject-context --part N --of M` left behind would go
// on injecting content from an older assembly forever. Reclaiming ctxloom's own
// callbacks by name is bounded and safe; reclaiming ANY command that merely
// invokes the ctxloom binary — which is what this used to do — is not, because
// the user is equally entitled to invoke it.
var ctxloomMachineCallbacks = []string{"inject-context", "session-bind", "stamp-plan", "hud"}

func isCtxloomMachineCallback(command string) bool {
	if !agent.IsManaged(command, "ctxloom") {
		return false
	}
	for _, sub := range ctxloomMachineCallbacks {
		if strings.Contains(command, sub) {
			return true
		}
	}
	return false
}

// managedHookDigests is every hook now present that ctxloom is claiming
// ownership of, as command DIGESTS — the set the ledger records for the next
// reconcile to remove. Digests, not commands: see agent.ComputeCommandDigest.
func (w *ClaudeCodeHookWriter) managedHookDigests(settings *claudeCodeSettings) []string {
	var out []string
	for _, matchers := range settings.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if hook.SCM != "" {
					out = append(out, agent.ComputeCommandDigest(hook.Command))
				}
			}
		}
	}
	return out
}

// removeCtxloomHooks drops the hooks ctxloom owns: those the ledger recorded
// last round, plus its own machine callbacks (see isCtxloomMachineCallback).
// Everything else — including a user's hand-authored hook that happens to
// invoke the ctxloom binary — is left exactly as found.
func (w *ClaudeCodeHookWriter) removeCtxloomHooks(settings *claudeCodeSettings, owned []string) {
	ownedSet := make(map[string]bool, len(owned))
	for _, d := range owned {
		ownedSet[d] = true
	}
	for eventName, matchers := range settings.Hooks {
		var filteredMatchers []claudeCodeHookMatcher
		for _, matcher := range matchers {
			var filteredHooks []claudeCodeHook
			for _, hook := range matcher.Hooks {
				// Keep hooks that are NOT ctxloom-managed
				if hook.SCM == "" && !ownedSet[agent.ComputeCommandDigest(hook.Command)] && !isCtxloomMachineCallback(hook.Command) {
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
func (w *ClaudeCodeHookWriter) addUnifiedHooks(settings *claudeCodeSettings, unified wire.UnifiedHooks) {
	agent.RouteUnifiedHooks("claude-code", []agent.HookRoute{
		{Hooks: unified.PreTool, Event: "PreToolUse"},
		{Hooks: unified.PostTool, Event: "PostToolUse"},
		{Hooks: unified.SessionStart, Event: "SessionStart"},
		{Hooks: unified.SessionEnd, Event: "SessionEnd"},
		{Hooks: unified.PreShell, Event: "PreToolUse", DefaultMatcher: "Bash"},
		{Hooks: unified.PostFileEdit, Event: "PostToolUse", DefaultMatcher: "Edit|Write"},
	}, func(event string, h wire.Hook) {
		w.addHook(settings, event, h)
	})
}

// addBackendHooks adds backend-specific passthrough hooks.
func (w *ClaudeCodeHookWriter) addBackendHooks(settings *claudeCodeSettings, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(settings, eventName, h)
		}
	}
}

// addHook adds a single hook to the settings for the given event.
func (w *ClaudeCodeHookWriter) addHook(settings *claudeCodeSettings, eventName string, h wire.Hook) {
	ccHook := claudeCodeHook{
		Type:    h.Type,
		Command: h.Command,
		Prompt:  h.Prompt,
		Timeout: h.Timeout,
		Async:   h.Async,
		SCM:     agent.ComputeHookHash(h),
	}

	// Default type to "command"
	if ccHook.Type == "" {
		ccHook.Type = "command"
	}

	// Drop any surviving entry with this exact command before appending.
	// removeCtxloomHooks only recognizes ctxloom-token commands; hooks ctxloom
	// writes for companion binaries (e.g. `ltk evaluate`, no marker possible
	// under Claude Code's strict settings schema) would otherwise duplicate on
	// every re-apply. Exact match keeps user variants (`ltk evaluate --config
	// ...`) untouched.
	w.removeExactCommand(settings, eventName, h.Command)

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

// removeExactCommand drops every hook entry under eventName whose command is
// exactly cmd, pruning emptied matchers. Companion-binary hooks carry no
// durable marker (strict schema), so identity is the verbatim command string.
func (w *ClaudeCodeHookWriter) removeExactCommand(settings *claudeCodeSettings, eventName, cmd string) {
	matchers := settings.Hooks[eventName]
	if len(matchers) == 0 {
		return
	}
	var keptMatchers []claudeCodeHookMatcher
	for _, m := range matchers {
		var kept []claudeCodeHook
		for _, hook := range m.Hooks {
			if hook.Command != cmd {
				kept = append(kept, hook)
			}
		}
		if len(kept) > 0 {
			m.Hooks = kept
			keptMatchers = append(keptMatchers, m)
		}
	}
	if len(keptMatchers) > 0 {
		settings.Hooks[eventName] = keptMatchers
	} else {
		delete(settings.Hooks, eventName)
	}
}

// AppMCPServerName is the name used for the ctxloom MCP server in settings.
const AppMCPServerName = agent.MCPServerName

// addMCPServersToConfig adds MCP servers from config to .mcp.json config.
func (w *ClaudeCodeHookWriter) addMCPServersToConfig(mcpConfig *claudeCodeMCPConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) {
	if mcpConfig.MCPServers == nil {
		mcpConfig.MCPServers = make(map[string]claudeCodeMCPServer)
	}

	// Auto-register ctxloom's own MCP server unless disabled. Command names
	// the self-exec absolute path (agent.CtxloomCommand) so this session's
	// MCP server can never diverge from the binary that materialized it.
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		mcpConfig.MCPServers[AppMCPServerName] = claudeCodeMCPServer{
			Command: agent.ResolveMCPCommand(w.mcpCommandOverride),
			Args:    agent.CtxloomMCPArgs,
			Cwd:     "${CLAUDE_PROJECT_DIR}", // Run in project directory so findAppDir works
			SCM:     "ctxloom-auto",          // Marker for auto-registered ctxloom server
		}
	}

	// Add MCP servers from profile bundles (loaded first, can be overridden)
	for name, server := range bundleMCP {
		mcpConfig.MCPServers[name] = claudeCodeMCPServer{
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
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
			Env:     server.Env,
			SCM:     agent.ComputeMCPServerHash(server), // Marker for ctxloom-managed
		}
	}

	// Add backend-specific MCP servers (passthrough)
	if backendServers, ok := mcp.Plugins["claude-code"]; ok {
		for name, server := range backendServers {
			mcpConfig.MCPServers[name] = claudeCodeMCPServer{
				Command: server.Command,
				Args:    server.Args,
				Env:     server.Env,
				SCM:     agent.ComputeMCPServerHash(server),
			}
		}
	}
}

// configExists answers "is this config file there?" without guessing.
// afero.Exists reports (false, err) for a path it could not STAT — a permission
// wall on the directory, an I/O failure — and reading that as "absent" makes
// every caller below lie: an uninstall becomes a silent no-op that reports
// success while ctxloom's hooks stay installed, and a status report claims
// nothing is installed over live config. Absent is only absent when the
// filesystem says so; a missing file is still (false, nil).
func configExists(fs afero.Fs, path string) (bool, error) {
	exists, err := afero.Exists(fs, path)
	if err != nil {
		return false, fmt.Errorf("cannot determine whether %s exists: %w", path, err)
	}
	return exists, nil
}

// RemoveSettings implements SettingsWriter for Claude Code: it clears
// ctxloom-managed hooks and statusline from settings.json and ctxloom-marked
// servers from .mcp.json, touching neither file when it does not already exist.
func (w *ClaudeCodeHookWriter) RemoveSettings(projectDir string) error {
	if err := w.removeSettingsFile(projectDir); err != nil {
		return err
	}
	return w.removeMCPConfig(projectDir)
}

// removeSettingsFile clears ctxloom-managed hooks and the managed statusline
// from settings.json under projectDir, preserving user-authored entries; a
// missing file is left absent. It is the settings.json half of RemoveSettings —
// same behavior — factored out so the delivery seam can revert the settings
// surface (hooks + statusline) independently of the .mcp.json surface.
func (w *ClaudeCodeHookWriter) removeSettingsFile(projectDir string) error {
	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)
	// See writeSettingsFile: same file, same lock, same race to close.
	return agent.WithFileLock(fs, settingsPath, func() error {
		exists, err := configExists(fs, settingsPath)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return fmt.Errorf("failed to load existing settings: %w", err)
		}
		led := ledger.Ledger{FS: fs, Dir: filepath.Dir(settingsPath), Warn: agent.Warn}
		owned, err := led.Read(ledger.SurfaceHooks)
		if err != nil {
			return err
		}
		w.removeCtxloomHooks(settings, owned)
		if settings.StatusLine != nil && agent.IsManaged(settings.StatusLine.Command, "ctxloom") {
			settings.StatusLine = nil
		}
		if err := w.saveSettings(settingsPath, settings); err != nil {
			return err
		}
		// Nothing of ctxloom's remains, so the claim must go too — a ledger naming
		// hooks that are gone would make the next reconcile delete whatever a user
		// later wrote under those commands.
		return led.Write(ledger.SurfaceHooks, nil)
	})
}

// removeMCPConfig strips ctxloom-marked servers from .mcp.json under projectDir,
// preserving user-defined servers; a missing file is left absent. It is the
// .mcp.json half of RemoveSettings — same behavior — factored out so the
// delivery seam can revert the MCP surface independently of settings.json.
func (w *ClaudeCodeHookWriter) removeMCPConfig(projectDir string) error {
	fs := w.getFS()
	mcpPath := w.MCPConfigPath(projectDir)
	// See writeMCPConfig: same file, same lock, same race to close.
	return agent.WithFileLock(fs, mcpPath, func() error {
		exists, err := configExists(fs, mcpPath)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for name, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				delete(mcpConfig.MCPServers, name)
			}
		}
		return w.saveMCPConfig(mcpPath, mcpConfig)
	})
}

// Status implements SettingsWriter for Claude Code.
func (w *ClaudeCodeHookWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus

	settingsPath := w.SettingsPath(projectDir)
	settingsExists, err := configExists(fs, settingsPath)
	if err != nil {
		return status, err
	}
	if settingsExists {
		status.SettingsExists = true
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing settings: %w", err)
		}
		status.HooksPresent = claudeHasManagedHook(settings)
		status.StatusLine = settings.StatusLine != nil && agent.IsManaged(settings.StatusLine.Command, "ctxloom")
	}

	mcpPath := w.MCPConfigPath(projectDir)
	mcpExists, err := configExists(fs, mcpPath)
	if err != nil {
		return status, err
	}
	if mcpExists {
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for _, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				status.MCPPresent = true
				break
			}
		}
	}
	return status, nil
}

// claudeHasManagedHook reports whether any configured hook is ctxloom-managed.
func claudeHasManagedHook(settings *claudeCodeSettings) bool {
	for _, matchers := range settings.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if hook.SCM != "" || agent.IsManaged(hook.Command, "ctxloom") {
					return true
				}
			}
		}
	}
	return false
}
