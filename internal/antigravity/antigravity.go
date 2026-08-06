// Package antigravity is ctxloom's Antigravity CLI (agy) agent: the
// settings/hooks writer that implements agent.SettingsWriter, plus the launch
// backend and the PreToolUse hook wire types other tools (ltk) consume.
//
// Antigravity splits workspace configuration across two files under .agents/:
// hooks.json (lifecycle hooks, nested matcher/hooks[] shape) and
// mcp_config.json (MCP servers) — see the wire types in hooks_wire.go for the
// payload/decision shapes.
//
// EVERY "verified" claim in this package is a DATED observation of a
// closed-source, fast-moving CLI, not a standing contract. The behaviour
// described here was probed live against agy v1.0.7 on 2026-06-10; the installed
// CLI is 1.1.5. Some v1.0.7 findings are already disproved (the flat `<name>.md`
// skills shape; the "no plan mode" claim, see backend.go), and agy's own bundled
// documentation now describes a WIDER hooks contract than the one modelled
// here. Re-verify against the installed version — live, or from agy's bundled
// docs under ~/.gemini/antigravity-cli/builtin/skills/agy-customizations/docs/
// — before relying on any claim below.
package antigravity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// NewWriter constructs the Antigravity CLI settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &AntigravityHookWriter{FS: o.FS}
}

// AppMCPServerName is the name under which ctxloom's own MCP server is
// registered in Antigravity's mcp_config.json.
const AppMCPServerName = agent.MCPServerName

// AgentsDir is the workspace directory Antigravity reads configuration from.
// It is shared territory: agy also writes subagent scratch space under it, and
// other tools use .agents/ too — ctxloom only manages hooks.json and
// mcp_config.json inside it.
const AgentsDir = ".agents"

// antigravityCtxloomHookName marks a hook entry as ctxloom-managed in the
// durable (serialized) name field. agy tolerates and preserves the field
// (probed on v1.0.7: a named entry loads and fires, reported as a "named hook").
// The field is NOT part of agy's documented handler contract — 1.1.5 documents
// type, command and timeout only — so ctxloom's ownership marker rides on
// tolerated-unknown-field behaviour, not on a guarantee.
const antigravityCtxloomHookName = "ctxloom-managed"

// AntigravityHookWriter writes hooks to Antigravity CLI's .agents/hooks.json
// and MCP servers to .agents/mcp_config.json.
type AntigravityHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// mcpCommandOverride, when non-empty, replaces agent.CtxloomCommand() as
	// the ctxloom-managed mcp_config.json entry's command (see
	// agent.ResolveMCPCommand) — set ONLY for an isolated-container cell.
	// Empty (the default) preserves the host self-exec-absolute behavior
	// exactly.
	mcpCommandOverride string
}

// getFS returns the filesystem to use, defaulting to the OS filesystem.
func (w *AntigravityHookWriter) getFS() afero.Fs {
	return agent.GetFS(w.FS)
}

// WorkspaceHooksPath returns the workspace-level .agents/hooks.json path — the
// only place agy was observed to read hooks from (v1.0.7). Exported for
// companion tools (ltk) that manage hooks in the same file, so the path
// convention has a single source of truth.
func WorkspaceHooksPath(dir string) string {
	return filepath.Join(dir, AgentsDir, "hooks.json")
}

// SettingsPath returns the path to Antigravity's project-level hooks.json file
// (the settings file ctxloom manages for Antigravity). No usable global location
// was found on v1.0.7: ~/.gemini/antigravity-cli/hooks.json is silently ignored,
// and a hooks.json under ~/.gemini/ or ~/.gemini/config/ hangs headless agy
// before any hook executes.
func (w *AntigravityHookWriter) SettingsPath(projectDir string) string {
	return WorkspaceHooksPath(projectDir)
}

// MCPConfigPath returns the path to Antigravity's project-level
// mcp_config.json file. agy reads MCP servers from this dedicated file, not
// from hooks.json or settings.json.
func (w *AntigravityHookWriter) MCPConfigPath(projectDir string) string {
	return filepath.Join(projectDir, AgentsDir, "mcp_config.json")
}

// antigravityHooksFile represents the structure of .agents/hooks.json.
type antigravityHooksFile struct {
	Hooks map[string][]antigravityHookGroup `json:"hooks,omitempty"`
	// Preserve other top-level fields.
	Other map[string]json.RawMessage `json:"-"`
}

// antigravityHookGroup is one entry in a hook event array: a matcher (a regex
// over agy tool names, e.g. "run_command|write_to_file") plus the command
// hooks that fire for it. This nested event → group → hooks[] shape is the
// verified agy schema. Unknown per-group fields a future agy adds are captured
// in extra on load and merged back on save, so a rewrite never drops them.
type antigravityHookGroup struct {
	Matcher string                 `json:"matcher,omitempty"`
	Hooks   []antigravityHookEntry `json:"hooks"`

	// extra holds unknown per-group fields for round-trip preservation.
	extra map[string]json.RawMessage
}

// antigravityHookGroupShape mirrors antigravityHookGroup's known fields
// without its methods, for recursion-free (un)marshalling.
type antigravityHookGroupShape antigravityHookGroup

// Known object keys per shape, DERIVED from the structs. The unknown-field
// capture below works by removing the known keys from the raw object, so the set
// it removes must be exactly the set of fields the shape declares — a
// hand-written list of tag names has no compiler link to the struct and drifts
// the moment a field is added.
var (
	hookGroupKnownKeys = jsonObjectKeys(antigravityHookGroupShape{})
	hookEntryKnownKeys = jsonObjectKeys(antigravityHookEntryShape{})
)

// jsonObjectKeys returns the object keys encoding/json reads and writes for
// shape's exported fields: the tag name when tagged, the field name otherwise,
// skipping `json:"-"`.
func jsonObjectKeys(shape any) []string {
	typ := reflect.TypeOf(shape)
	keys := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
	}
	return keys
}

// UnmarshalJSON decodes the known fields and captures unknown keys in extra.
func (g *antigravityHookGroup) UnmarshalJSON(data []byte) error {
	var shape antigravityHookGroupShape
	if err := json.Unmarshal(data, &shape); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, known := range hookGroupKnownKeys {
		delete(raw, known)
	}
	*g = antigravityHookGroup(shape)
	if len(raw) > 0 {
		g.extra = raw
	}
	return nil
}

// MarshalJSON emits the known fields, merged with any preserved unknown keys.
// Known fields win on a key collision (extra never overrides them).
func (g antigravityHookGroup) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(antigravityHookGroupShape(g), g.extra)
}

// antigravityHookEntry is a single command hook. agy requires type:"command".
// name is a durable field agy preserves, used to identify ctxloom-managed
// entries for clean removal. The timeout field is deliberately never written:
// agy documents it as seconds with its own 30s default (1.1.5 hooks contract),
// and ctxloom has no better number to impose than the engine's. An existing
// user-set timeout round-trips untouched. Unknown per-entry fields are
// captured in extra on load and merged back on save (see antigravityHookGroup).
type antigravityHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Timeout int    `json:"timeout,omitempty"`

	// extra holds unknown per-entry fields for round-trip preservation.
	extra map[string]json.RawMessage
}

// antigravityHookEntryShape mirrors antigravityHookEntry's known fields
// without its methods, for recursion-free (un)marshalling.
type antigravityHookEntryShape antigravityHookEntry

// UnmarshalJSON decodes the known fields and captures unknown keys in extra.
func (e *antigravityHookEntry) UnmarshalJSON(data []byte) error {
	var shape antigravityHookEntryShape
	if err := json.Unmarshal(data, &shape); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, known := range hookEntryKnownKeys {
		delete(raw, known)
	}
	*e = antigravityHookEntry(shape)
	if len(raw) > 0 {
		e.extra = raw
	}
	return nil
}

// MarshalJSON emits the known fields, merged with any preserved unknown keys.
func (e antigravityHookEntry) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(antigravityHookEntryShape(e), e.extra)
}

// marshalWithExtra marshals shape, merging extra's unknown keys into the
// object. Without extras the shape marshals as-is, so entries ctxloom itself
// writes keep their exact current byte shape (canonicalization downstream is
// unchanged either way — agent.CanonicalJSON re-sorts keys recursively).
func marshalWithExtra(shape any, extra map[string]json.RawMessage) ([]byte, error) {
	known, err := json.Marshal(shape)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return known, nil
	}
	merged := make(map[string]json.RawMessage, len(extra)+4)
	if err := json.Unmarshal(known, &merged); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, ok := merged[k]; !ok {
			merged[k] = v
		}
	}
	return json.Marshal(merged)
}

// antigravityMCPFile represents the structure of .agents/mcp_config.json.
// Server entries are kept raw so fields ctxloom doesn't model (serverUrl for
// remote servers, headers, future additions) survive a rewrite byte-for-byte.
// WriteSettings implements SettingsWriter for Antigravity CLI: hooks to
// hooks.json, MCP servers to mcp_config.json.
//
// CAUTION carried from live verification: a registered stdio MCP server whose
// process cannot complete the MCP handshake hangs headless agy. ctxloom only
// registers real MCP servers, but never write placeholder/dummy entries here.
func (w *AntigravityHookWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}

	fs := w.getFS()
	hooksPath := w.SettingsPath(projectDir)

	if err := fs.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %w", AgentsDir, err)
	}

	hooksFile, err := w.loadHooksFile(hooksPath)
	if err != nil {
		return fmt.Errorf("failed to load existing hooks.json: %w", err)
	}

	w.removeCtxloomHooks(hooksFile)
	contextHash := w.addUnifiedHooks(hooksFile, hooks.Unified)
	if backendHooks, ok := hooks.Plugins["antigravity"]; ok {
		w.addBackendHooks(hooksFile, backendHooks)
	}

	if err := w.saveHooksFile(hooksPath, hooksFile); err != nil {
		return err
	}

	if err := w.reconcileManagedContext(projectDir, contextHash); err != nil {
		return err
	}

	return w.mcpFile(projectDir).WriteServers(mcp, bundleMCP)
}

// loadHooksFile loads existing hooks.json, or an empty structure for a
// MISSING file — the only case that legitimately means "nothing configured".
//
// A parse failure is refused, not tolerated. The old contract ("ctxloom never
// blocks launch on a schema change") described the intent but not the effect:
// saveHooksFile persists whatever this returns, so continuing with an empty
// structure replaced the user's entire hooks.json with a ctxloom-only file.
// The same defect was fixed in the claude writer; this is its twin. Both now
// route through agent.RefuseCorrupt, which backs the original bytes up and
// points the user at them.
func (w *AntigravityHookWriter) loadHooksFile(path string) (*antigravityHooksFile, error) {
	hf := &antigravityHooksFile{
		Hooks: make(map[string][]antigravityHookGroup),
		Other: make(map[string]json.RawMessage),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return hf, nil
		}
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, agent.RefuseCorrupt(fs, path, data,
			AgentsDir+"/hooks.json (schema may have changed)", err, "to avoid overwriting it")
	}

	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &hf.Hooks); err != nil {
			return nil, agent.RefuseCorrupt(fs, path, data,
				"hooks in "+AgentsDir+"/hooks.json", err, "to avoid dropping existing hooks")
		}
		delete(raw, "hooks")
	}

	hf.Other = raw
	return hf, nil
}

// warn outputs a warning message to stderr.
func (w *AntigravityHookWriter) warn(format string, args ...interface{}) {
	agent.Warn(format, args...)
}

// saveHooksFile writes hooks.json back atomically.
func (w *AntigravityHookWriter) saveHooksFile(path string, hf *antigravityHooksFile) error {
	output := make(map[string]interface{})
	for k, v := range hf.Other {
		// hf.Other's values are always syntactically valid JSON — they came
		// from a successful json.Unmarshal into map[string]json.RawMessage in
		// loadHooksFile — so the canonicaliser can re-emit them as-is and
		// there is nothing here that can fail. Passing the ORIGINAL bytes is
		// also what keeps them exact: a decode into interface{} first would
		// round every number past float64's exact range
		// (1234567890123456789 → 1234567890123456800), rewriting a file the
		// user authored with no warning and a success exit code.
		output[k] = json.RawMessage(v)
	}

	if len(hf.Hooks) > 0 {
		output["hooks"] = hf.Hooks
	}

	if len(output) == 0 {
		// Nothing to persist (no managed hooks, no preserved fields). Mirror
		// saveMCPFile: never create a stray empty `{}` file — only rewrite/clear
		// an existing one (e.g. RemoveSettings). For a context-only profile the
		// injection hook is diverted to AGENTS.md, leaving hooks empty; agy
		// treats an absent file and `{}` identically, so skip the write.
		//
		// A failed check is not an answer: skipping the write on it would decide
		// "no file to clear" from an error that means the opposite is possible.
		exists, err := afero.Exists(w.getFS(), path)
		if err != nil {
			return fmt.Errorf("failed to check %s/hooks.json: %w", AgentsDir, err)
		}
		if !exists {
			return nil
		}
	}

	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal hooks.json: %w", err)
	}
	return agent.AtomicWriteFile(w.getFS(), path, data, "hooks.json")
}

// removeCtxloomHooks removes ctxloom-managed hooks, descending the
// event → group → hooks[] shape. An entry is ctxloom-managed if its durable
// name marker matches, or (defensively) its command's executable token is
// ctxloom. Groups left with no entries are dropped, and events left with no
// groups are removed.
func (w *AntigravityHookWriter) removeCtxloomHooks(hf *antigravityHooksFile) {
	for eventName, groups := range hf.Hooks {
		var keptGroups []antigravityHookGroup
		for _, g := range groups {
			var keptEntries []antigravityHookEntry
			for _, e := range g.Hooks {
				if e.Name == antigravityCtxloomHookName || agent.IsManaged(e.Command, "ctxloom") {
					continue
				}
				keptEntries = append(keptEntries, e)
			}
			if len(keptEntries) > 0 {
				g.Hooks = keptEntries
				keptGroups = append(keptGroups, g)
			}
		}
		if len(keptGroups) > 0 {
			hf.Hooks[eventName] = keptGroups
		} else {
			delete(hf.Hooks, eventName)
		}
	}
}

// Antigravity tool-name matchers for the scoped unified events. The matcher is
// a regex over agy tool names (verified: alternation and ".*" both work).
// Built from the hooks_wire.go tool-name constants so the matcher vocabulary
// cannot drift from the verified wire vocabulary.
const (
	// antigravityShellMatcher scopes a hook to shell execution tools.
	antigravityShellMatcher = ToolRunCommand + "|" + ToolExecuteCommand
	// antigravityFileEditMatcher scopes a hook to file mutation tools.
	antigravityFileEditMatcher = ToolWriteToFile + "|" + ToolReplaceFileContent
)

// addUnifiedHooks translates unified hooks to Antigravity's events and
// returns the context hash when a context-injection hook was present.
//
// agy v1.0.7 loads PreToolUse, PostToolUse, and Stop handlers. SessionStart /
// SessionEnd entries are written through verbatim: agy silently skips events
// it doesn't support (verified — they don't load as handlers and don't error),
// and they light up if a future agy adds the events. The one exception is the
// context-injection hook (ContextHash set): registering it would silently
// drop ctxloom's assembled context, so it is diverted — WriteSettings
// materializes the context into .agents/AGENTS.md, which agy reads at
// session start (verified). Chunked injection (N part-hooks for one hash) is
// a hook-harness workaround; the file carries the whole content once.
func (w *AntigravityHookWriter) addUnifiedHooks(hf *antigravityHooksFile, unified wire.UnifiedHooks) string {
	// SessionStart cannot ride the shared route table: the context-injection
	// hook is diverted to .agents/AGENTS.md and idempotent hooks divert to
	// PreToolUse (see the doc comment above).
	contextHash := ""
	for _, h := range unified.SessionStart {
		if h.ContextHash != "" {
			contextHash = h.ContextHash
			continue
		}
		// A hook declared idempotent falls back to PreToolUse (first tool
		// call and after) — the only way it ever fires on agy. Diverted, not
		// duplicated, so a future agy adding SessionStart can't double-fire.
		if h.PreToolFallback {
			hook := h
			if hook.Matcher == "" {
				hook.Matcher = ".*"
			}
			w.addHook(hf, EventPreToolUse, hook)
			continue
		}
		w.addHook(hf, "SessionStart", h)
	}
	agent.RouteUnifiedHooks("antigravity", []agent.HookRoute{
		{Hooks: unified.SessionEnd, Event: "SessionEnd"},
		{Hooks: unified.PreTool, Event: EventPreToolUse},
		{Hooks: unified.PostTool, Event: EventPostToolUse},
		{Hooks: unified.PreShell, Event: EventPreToolUse, DefaultMatcher: antigravityShellMatcher},
		{Hooks: unified.PostFileEdit, Event: EventPostToolUse, DefaultMatcher: antigravityFileEditMatcher},
	}, func(event string, h wire.Hook) {
		w.addHook(hf, event, h)
	})
	return contextHash
}

// addBackendHooks adds backend-specific passthrough hooks.
func (w *AntigravityHookWriter) addBackendHooks(hf *antigravityHooksFile, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(hf, eventName, h)
		}
	}
}

// addHook adds a single hook for the given event in agy's nested
// group → hooks[] shape with type:"command" and the ctxloom name marker.
//
// Only command hooks are written: agy executes type:"command" entries only,
// and translating a prompt/agent hook here would mangle it into
// {"type":"command","command":""} — a dead entry. Non-command hooks are
// skipped with a warning (empty Type is the wire default and means command).
//
// A hook with no command is skipped for the same reason, and the Type guard
// above does not cover it: `command` is required in agy's hook contract, and a
// prompt hook authored WITHOUT an explicit type reads as a command hook with
// nothing to run — an entry agy loads as a live handler that executes nothing.
func (w *AntigravityHookWriter) addHook(hf *antigravityHooksFile, eventName string, h wire.Hook) {
	if h.Type != "" && h.Type != "command" {
		w.warn("skipping %s hook of type %q: antigravity only supports command hooks", eventName, h.Type)
		return
	}
	if h.Command == "" {
		w.warn("skipping %s hook with no command: antigravity would load it as a live hook that runs nothing (a prompt-only hook has no antigravity equivalent)", eventName)
		return
	}

	// Drop any surviving entry with this exact command before appending.
	// removeCtxloomHooks only recognizes the name marker / ctxloom-token
	// commands; an identical command installed by a companion binary itself
	// (e.g. ltk registered `ltk evaluate ...` directly, without the marker)
	// would otherwise fire twice. Exact match keeps user variants of the same
	// binary with different args untouched — same semantics as the claude
	// writer's removeExactCommand dedupe.
	w.removeExactCommand(hf, eventName, h.Command)

	entry := antigravityHookEntry{
		Type:    "command",
		Command: h.Command,
		Name:    antigravityCtxloomHookName,
	}
	group := antigravityHookGroup{Matcher: h.Matcher, Hooks: []antigravityHookEntry{entry}}
	hf.Hooks[eventName] = append(hf.Hooks[eventName], group)
}

// removeExactCommand drops every hook entry under eventName whose command is
// exactly cmd, pruning emptied groups and events. Identity is the verbatim
// command string: entries written by other tools carry no ctxloom marker, so
// this is the only way to recognize an exact duplicate of a hook ctxloom is
// about to add.
func (w *AntigravityHookWriter) removeExactCommand(hf *antigravityHooksFile, eventName, cmd string) {
	if cmd == "" {
		return
	}
	groups := hf.Hooks[eventName]
	if len(groups) == 0 {
		return
	}
	var keptGroups []antigravityHookGroup
	for _, g := range groups {
		var kept []antigravityHookEntry
		for _, e := range g.Hooks {
			if e.Command != cmd {
				kept = append(kept, e)
			}
		}
		if len(kept) > 0 {
			g.Hooks = kept
			keptGroups = append(keptGroups, g)
		}
	}
	if len(keptGroups) > 0 {
		hf.Hooks[eventName] = keptGroups
	} else {
		delete(hf.Hooks, eventName)
	}
}

// mcpFile binds the shared MCP-registry reconciler (agent.MCPFileConfig —
// load/save/ledger/reconcile live there, shared with kiro) to this project's
// mcp_config.json + sidecar ledger.
func (w *AntigravityHookWriter) mcpFile(projectDir string) agent.MCPFileConfig {
	return agent.MCPFileConfig{
		FS:              w.getFS(),
		Path:            w.MCPConfigPath(projectDir),
		LedgerDir:       filepath.Join(projectDir, AgentsDir),
		Label:           AgentsDir + "/mcp_config.json",
		PluginKey:       "antigravity",
		Warn:            w.warn,
		CommandOverride: w.mcpCommandOverride,
	}
}

// Managed-context section markers in .agents/AGENTS.md. agy reads
// .agents/AGENTS.md at session start (verified), which makes it the delivery
// channel for ctxloom's assembled context — agy fires no SessionStart hooks,
// so the injection hook other agents use can never run here. Content between
// the markers is ctxloom-owned and reconciled on every apply; anything
// outside them is the user's and preserved byte-for-byte.
//
// CONCURRENCY LIMIT (delegated agent_run fan-out): .agents/AGENTS.md is a WORKSPACE-FIXED
// path, and agy exposes no per-invocation context redirect (no --mcp-config /
// --settings / --append-system-prompt equivalent). So N antigravity agents run
// in one cwd would each rewrite this one file — last writer wins → cross-agent
// context bleed/clobber. Unlike claude (per-invocation flags) and kiro (per-agent
// agent-JSON `--agent`), antigravity has NO redirection lever, so per-agent
// CONCURRENT isolation requires a per-agent cwd (git worktree) or container.
// See the per-agent-config-delivery memory note (ISOLATION AXIS).
// managedContextBegin/End alias the shared markers (internal/shared/agent) so
// this package's existing references (including its tests) keep working
// unchanged after the marker-merge logic moved to the shared core.
const (
	managedContextBegin = agent.ManagedContextBegin
	managedContextEnd   = agent.ManagedContextEnd
)

// agentsMDPath returns the path to the workspace .agents/AGENTS.md file.
func (w *AntigravityHookWriter) agentsMDPath(projectDir string) string {
	return filepath.Join(projectDir, AgentsDir, "AGENTS.md")
}

// WriteContext implements agent.ContextWriter for Antigravity: it merges the
// assembled context (req.Context) into the managed section of .agents/AGENTS.md
// — agy reads that file at session start and fires no SessionStart hook for
// context — preserving any user content outside the markers. Empty content
// removes the managed section (and the file, when it was wholly ctxloom's).
func (w *AntigravityHookWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	return w.writeManagedContext(req.ProjectDir, req.Context)
}

// reconcileManagedContext writes (or removes, when hash is empty) the managed
// context section of .agents/AGENTS.md. The context content is read from the
// content-addressed context file the provider wrote under projectDir, then
// merged by the shared writeManagedContext core.
func (w *AntigravityHookWriter) reconcileManagedContext(projectDir, hash string) error {
	content := ""
	if hash != "" {
		var err error
		content, err = agent.ReadContextFile(projectDir, hash, agent.WithContextFS(w.getFS()))
		if err != nil {
			// NOT fault-tolerance — this used to warn and continue with "",
			// and "" is how the caller says "deliver no context", so
			// writeManagedContext STRIPPED the managed section (removing
			// AGENTS.md when nothing user-authored remained) and WriteSettings
			// returned nil. The agent launched with zero delivered context and
			// the last good delivery was destroyed on the way out. A non-empty
			// hash asserts context exists; failing to resolve it means we could
			// not determine what to deliver, which is not the same as being
			// asked to deliver nothing. hash == "" stays the legitimate
			// nothing-to-do (and is how teardown strips the section).
			return fmt.Errorf("antigravity context delivery: cannot resolve context %s — refusing to strip the managed section and launch with no context: %w", hash, err)
		}
	}
	_, err := w.writeManagedContext(projectDir, content)
	return err
}

// writeManagedContext is the marker-merge core shared by reconcileManagedContext
// (hash-addressed) and WriteContext (string-addressed). It replaces the
// ctxloom-managed marker section of .agents/AGENTS.md with content (when
// non-empty), preserving user content outside the markers; empty content strips
// the section, removing the file when nothing user-authored remains. It reports
// the workspace-relative path written or removed.
//
// The merge itself is the shared core (agent.WriteManagedContext) — the same
// implementation claude's CLAUDE.md and codex's AGENTS.md now use, ported from
// here (this was the ORIGINAL and only implementation before the extraction).
func (w *AntigravityHookWriter) writeManagedContext(projectDir, content string) (agent.ContextReport, error) {
	path := w.agentsMDPath(projectDir)
	rel := filepath.Join(AgentsDir, "AGENTS.md")
	return agent.WriteManagedContext(w.getFS(), path, rel, content, "AGENTS.md")
}

// RemoveSettings implements SettingsWriter for Antigravity CLI: it clears
// ctxloom-managed hooks from hooks.json, the managed MCP server from
// mcp_config.json, and the managed context section from .agents/AGENTS.md,
// leaving absent files absent.
func (w *AntigravityHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()

	hooksPath := w.SettingsPath(projectDir)
	// An unreadable hooks.json is NOT an absent one: reporting success here
	// would tell the user their managed hooks were cleared when nothing was
	// even read.
	exists, err := afero.Exists(fs, hooksPath)
	if err != nil {
		return fmt.Errorf("failed to check %s/hooks.json: %w", AgentsDir, err)
	}
	if exists {
		hf, err := w.loadHooksFile(hooksPath)
		if err != nil {
			return fmt.Errorf("failed to load existing hooks.json: %w", err)
		}
		w.removeCtxloomHooks(hf)
		if err := w.saveHooksFile(hooksPath, hf); err != nil {
			return err
		}
	}

	if err := w.mcpFile(projectDir).RemoveServers(); err != nil {
		return err
	}

	return w.reconcileManagedContext(projectDir, "")
}

// Status implements SettingsWriter for Antigravity CLI.
func (w *AntigravityHookWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus

	hooksPath := w.SettingsPath(projectDir)
	// Reported status must never be a guess: an unreadable hooks.json would
	// otherwise read as an unconfigured project.
	exists, err := afero.Exists(fs, hooksPath)
	if err != nil {
		return status, fmt.Errorf("failed to check %s/hooks.json: %w", AgentsDir, err)
	}
	if exists {
		status.SettingsExists = true
		hf, err := w.loadHooksFile(hooksPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing hooks.json: %w", err)
		}
		status.HooksPresent = antigravityHasManagedHook(hf)
	}

	mcpPresent, err := w.mcpFile(projectDir).ManagedPresent()
	if err != nil {
		return status, err
	}
	status.MCPPresent = mcpPresent

	// The managed context section is Antigravity's stand-in for the
	// SessionStart injection hook other agents carry, so it counts as a
	// managed hook for wired-status purposes. An absent file means "no managed
	// context"; any other read failure means the answer is unknown, which is not
	// the same thing.
	data, err := afero.ReadFile(fs, w.agentsMDPath(projectDir))
	switch {
	case err == nil:
		if strings.Contains(string(data), managedContextBegin) {
			status.HooksPresent = true
		}
	case !os.IsNotExist(err):
		return status, fmt.Errorf("failed to read %s/AGENTS.md: %w", AgentsDir, err)
	}
	return status, nil
}

// antigravityHasManagedHook reports whether any configured hook is
// ctxloom-managed, descending the event → group → hooks[] shape.
func antigravityHasManagedHook(hf *antigravityHooksFile) bool {
	for _, groups := range hf.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Name == antigravityCtxloomHookName || agent.IsManaged(hook.Command, "ctxloom") {
					return true
				}
			}
		}
	}
	return false
}
