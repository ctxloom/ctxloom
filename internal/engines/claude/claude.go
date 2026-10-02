// Package claude is ctxloom's Claude Code engine: the kind (definition.go),
// its instance (instance.go), its backend (claudecode.go) and the
// settings/hooks writer that implements agent.SettingsWriter (this file).
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/exectoken"

	hew "github.com/benjaminabbitt/hew/go"
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// NewWriter constructs Claude Code's settings writer: the status read
// (agent.SettingsReader) and the CLAUDE.md context write.
func NewWriter(o agent.SettingsOptions) agent.SettingsReader {
	return &ClaudeCodeHookWriter{FS: o.FS, projectClaims: o.ProjectClaims}
}

// ClaudeCodeHookWriter writes hooks to Claude Code's settings.json format.
type ClaudeCodeHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// projectClaims is the ownership record's account of what the project
	// writer has installed (agent.SettingsOptions.ProjectClaims).
	projectClaims func(target string) ([]string, error)
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
// Other keeps every key this struct does not model verbatim — the statusLine
// schema is Claude Code's, not ctxloom's, and a key it adds must not vanish on
// ctxloom's next write (the claudeCodePermissions.Other idiom).
type claudeCodeStatusLine struct {
	Type    string                     `json:"type"`
	Command string                     `json:"command"`
	Padding int                        `json:"padding,omitempty"`
	Other   map[string]json.RawMessage `json:"-"`
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
	// Args is claude's exec form: Command is resolved as an executable and
	// spawned with these arguments, no shell (wire.Hook.Args).
	Args    []string `json:"args,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
	Async   bool     `json:"async,omitempty"`
	SCM     string   `json:"-"` // Internal only - not serialized (Claude Code strict schema validation)
}

// line is the hook's command identity — the whole argv as one shell line
// (wire.Hook.Line). In exec form every hook ctxloom writes for itself names
// the same executable, so Command alone would make them all one hook.
func (h claudeCodeHook) line() string {
	return wire.Hook{Command: h.Command, Args: h.Args}.Line()
}

// ContextPath returns the path to Claude Code's native context file
// (<projectDir>/CLAUDE.md). Sibling of SettingsPath/MCPConfigPath, added for
// the read half (contextSurface.State in surfaces.go) so it shares the exact
// path the write half (WriteContext, below) uses rather than a second
// filepath.Join literal.
func (w *ClaudeCodeHookWriter) ContextPath(projectDir string) string {
	return filepath.Join(projectDir, ContextFileName)
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
// The marker merge itself is the shared core (agent.WriteManagedContext), so
// every backend that owns a human-editable context file shares one merge.
func (w *ClaudeCodeHookWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	path := w.ContextPath(req.ProjectDir)
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
	// The typed decode above succeeding means raw is an object.
	if err := json.Unmarshal(raw, &sl.Other); err != nil {
		return nil, w.corruptSettings(path, data, "statusLine", err, "to avoid replacing or deleting the statusline already in it")
	}
	for _, k := range []string{"type", "command", "padding"} {
		delete(sl.Other, k)
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
// applyMCP routes through here precisely so no future field can be added
// with a warn-and-continue branch — a warning is not a guard, and each of the
// paths that had one (permissions, permissions.deny, .mcp.json) was
// destroying user data behind it.
func (w *ClaudeCodeHookWriter) corruptSettings(path string, data []byte, what string, cause error, consequence string) error {
	return agent.RefuseCorrupt(w.getFS(), path, data, what, cause, consequence)
}

// desiredMCPServers is the set of servers ctxloom wants present, as generic
// values — the user's own servers are never part of it. Each entry is spelled
// by the shared agent.ChatMCPConfigEntryOf, so a remote server lands here as
// type/url/headers exactly as the chat scratch file spells it, and a
// targetless one is refused by name.
//
// The JSON round trip is what honours the entry's json tags, including
// omitempty: hew encodes whatever Go value it is handed, and handing it the
// struct directly would spell the keys by their Go field names.
func (w *ClaudeCodeHookWriter) desiredMCPServers(bundleMCP map[string]wire.MCPServer) (map[string]any, error) {
	entries, err := w.mcpEntries(bundleMCP)
	if err != nil {
		return nil, err
	}

	out := make(map[string]any, len(entries))
	for name, entry := range entries {
		generic, err := agent.GenericMCPEntry(name, entry)
		if err != nil {
			return nil, err
		}
		out[name] = generic
	}
	return out, nil
}

// applyMCPServers is the one write into a "mcpServers" table: it puts desired
// there through store, taking back out what the store's writer put there last
// time, and records what it wrote. Both of this package's writers — ctxloom's
// own hook writer and taskloom's registrar — go through it, so the two never
// disagree about how the table is patched.
//
// owned names the servers the caller manages, so an entry the caller itself
// wrote that no record covers is taken back out rather than replaced in place
// — see confpatch.WithOwnedPaths.
func applyMCPServers(fs afero.Fs, store *confpatch.Store, mcpPath string, desired map[string]any, owned []string, opts ...confpatch.ApplyOption) (confpatch.Result, error) {
	ownedPaths := make([]string, 0, len(owned))
	for _, name := range owned {
		ownedPaths = append(ownedPaths, "/"+mcpServersKey+"/"+name)
	}
	opts = append(opts, confpatch.WithOwnedPaths(ownedPaths...))
	res, err := store.Apply(fs, mcpPath, func(doc *hew.Doc, cur hew.Document) (int, error) {
		if len(desired) == 0 {
			return 0, nil
		}
		// Addressing /mcpServers/<name> against a file that has no mcpServers
		// is HEW013 no-match, so state the container whole when it is absent.
		if _, ok := cur.Root().Member(mcpServersKey); !ok {
			p, perr := hew.ParsePathIn(doc.Format(), "/"+mcpServersKey)
			if perr != nil {
				return 0, perr
			}
			doc.AtPath(p).Set(desired)
			return 1, nil
		}
		recorded := 0
		for _, name := range collections.SortedKeys(desired) { // stable order: a deterministic record
			p, perr := hew.ParsePathIn(doc.Format(), "/"+mcpServersKey+"/"+name)
			if perr != nil {
				return 0, perr
			}
			doc.AtPath(p).Set(desired[name])
			recorded++
		}
		return recorded, nil
	}, opts...)
	if len(res.HealedPaths) > 0 {
		// Say it. The entry was rewritten by a DIFFERENT copy of the writer
		// (the binary on PATH versus one built in a working tree, which write
		// different absolute paths), and this one has just taken the other's
		// entry out. That is not an error and must not read as one, but a
		// file changing under the user for a reason nothing else names is
		// worth a line — it is also the signal that two copies are managing
		// one project.
		clidiag.Warn("ctxloom", "%s: took over %s, which a different binary had written; if that is unexpected, check which binary you are running",
			mcpPath, strings.Join(res.HealedPaths, ", "))
	}
	if err != nil {
		// An unparseable .mcp.json reaches here as a hew open failure, and the
		// user is owed more than a refusal: the file is BACKED UP before
		// declining, exactly as the pre-hew loader did. Refusing already
		// guarantees nothing is destroyed — the backup is what makes the
		// original recoverable if the user cannot see what broke it.
		if data, rerr := afero.ReadFile(fs, mcpPath); rerr == nil {
			var probe agent.ChatMCPConfigDoc
			if jerr := json.Unmarshal(data, &probe); jerr != nil {
				return res, agent.RefuseCorrupt(fs, mcpPath, data, MCPFileName, jerr,
					"to avoid deleting the MCP servers already in it")
			}
		}
		return res, fmt.Errorf("failed to write %s: %w", mcpPath, err)
	}
	return res, nil
}

// mcpServersKey is the one container ctxloom writes into in .mcp.json.
const mcpServersKey = "mcpServers"

// ctxloomStatusLineCommand is the exact statusline command ctxloom installs —
// one definition, so the writer and the ownership check can never disagree.
func ctxloomStatusLineCommand() string {
	return agent.CtxloomCommand() + " hook hud"
}

// unifiedHookRoutes maps each unified hook kind to claude's native event.
func unifiedHookRoutes(unified wire.UnifiedHooks) []agent.HookRoute {
	return []agent.HookRoute{
		{Hooks: unified.PreTool, Event: "PreToolUse"},
		{Hooks: unified.PostTool, Event: "PostToolUse"},
		{Hooks: unified.SessionStart, Event: "SessionStart"},
		{Hooks: unified.SessionEnd, Event: "SessionEnd"},
		// Stop and UserPromptSubmit take no matcher: neither event has a tool
		// to match against, so the routes declare none and a hook that carried
		// one is emitted without it.
		{Hooks: unified.TurnEnd, Event: "Stop"},
		{Hooks: unified.TurnStart, Event: HookEventUserPromptSubmit},
		{Hooks: unified.PreShell, Event: "PreToolUse", DefaultMatcher: "Bash"},
		{Hooks: unified.PostFileEdit, Event: "PostToolUse", DefaultMatcher: "Edit|Write"},
		{Hooks: unified.PermissionAsk, Event: hookEventPermissionRequest},
	}
}

// AppMCPServerName is the name used for the ctxloom MCP server in settings.
const AppMCPServerName = agent.MCPServerName

// mcpEntries renders the resolved server set as .mcp.json entries, each
// written AS DECLARED by the bundle that shipped it — ctxloom's own entry
// (its companion loadout's) names the bare ctxloom executable, looked up on
// PATH wherever the file is read (agent.CtxloomCommand's invariant), and no
// rewrite happens here. The ctxloom entry alone gets a cwd, so it runs in the
// project directory where findAppDir works — the one field no bundle can
// express, and .mcp.json (not settings.json) is where ${CLAUDE_PROJECT_DIR}
// expands, see MCPConfigPath.
func (w *ClaudeCodeHookWriter) mcpEntries(bundleMCP map[string]wire.MCPServer) (map[string]agent.ChatMCPConfigEntry, error) {
	out := make(map[string]agent.ChatMCPConfigEntry)
	for name, server := range bundleMCP {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		entry, err := agent.ChatMCPConfigEntryOf(agent.ChatMCPServerFromWire(name, server))
		if err != nil {
			return nil, err
		}
		if name == AppMCPServerName {
			entry.Cwd = "${CLAUDE_PROJECT_DIR}"
		}
		out[name] = entry
	}
	return out, nil
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
		status.StatusLine = settings.StatusLine != nil && exectoken.IsManaged(settings.StatusLine.Command, "ctxloom")
	}

	mcpPath := w.MCPConfigPath(projectDir)
	mcpExists, err := configExists(fs, mcpPath)
	if err != nil {
		return status, err
	}
	if mcpExists {
		mcp, err := w.mcpPresent(mcpPath)
		if err != nil {
			return status, err
		}
		status.MCPPresent = mcp
	}
	return status, nil
}

// mcpPresent asks the claims RECORD what the project writer put in mcpPath,
// not the file: scanning the user's file could not tell an entry ctxloom
// created from one a user copied out of ctxloom's.
func (w *ClaudeCodeHookWriter) mcpPresent(mcpPath string) (bool, error) {
	if w.projectClaims == nil {
		return false, nil
	}
	live, err := w.projectClaims(mcpPath)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(live, func(p string) bool { return strings.HasPrefix(p, present.PointerKey(mcpServersKey)+"/") }), nil
}

// claudeHasManagedHook reports whether any configured hook is ctxloom-managed.
func claudeHasManagedHook(settings *claudeCodeSettings) bool {
	for _, matchers := range settings.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if hook.SCM != "" || exectoken.IsManaged(hook.line(), "ctxloom") {
					return true
				}
			}
		}
	}
	return false
}
