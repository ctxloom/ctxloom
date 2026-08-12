// Package codex is ctxloom's OpenAI Codex CLI agent: the settings/hooks writer
// (agent.SettingsWriter) and the launch backend (agent.Backend), mirroring the
// claude and gemini agent modules. Codex reads project-scoped config from
// .codex/config.toml in a trusted project; ctxloom manages the [hooks] and
// [mcp_servers] tables there and preserves every other key the user set.
//
// ┌─────────────────────────────────────────────────────────────────────────┐
// │ WARNING — IMPLEMENTED AGAINST DOCS, ENTIRELY UNTESTED.                     │
// │                                                                           │
// │ Every codex-specific detail in this module — the config.toml [hooks] and  │
// │ [mcp_servers] format, the `codex exec` + --sandbox/--ask-for-approval     │
// │ flags, the global ~/.codex/prompts location and frontmatter, and the      │
// │ hook stdin fields used for session registration — is derived solely from  │
// │ the published OpenAI Codex CLI documentation. None of it has been run      │
// │ against a real codex binary: the maintainer's Linux development platform   │
// │ has no codex access. Unit tests cover the Go logic, NOT codex's actual     │
// │ acceptance of what we emit.                                               │
// │                                                                           │
// │ Treat every format/flag here as PROVISIONAL until smoke-tested on a       │
// │ machine with codex installed (write hooks/MCP, launch, confirm the hooks  │
// │ fire and the MCP server connects, run a oneshot/distill).                 │
// └─────────────────────────────────────────────────────────────────────────┘
//
// STATUS (2026-07-10): still LIVE-UNTESTED end-to-end — never run against a
// real codex account (no OPENAI_API_KEY/CODEX_API_KEY on any dev host).
// Proven since the warning above was written: hermetic backend parity
// (TestStartRun_BackendParity) and CLI/JSON-RPC-level probing (codex-acp
// advertises loadSession:true; `-c model=` is accepted at parse). NOT proven:
// a live authenticated delegated echo — parse-acceptance is not honor.
// Revive once codex credentials exist on a dev host (taskloom bold-smirk).
package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// NewWriter constructs the Codex CLI settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &CodexHookWriter{FS: o.FS}
}

// CodexHookWriter writes hooks and MCP servers to Codex's project-level
// .codex/config.toml. The file is TOML; ctxloom owns the [hooks] and
// [mcp_servers] tables and round-trips every other top-level key untouched.
type CodexHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// MCPCommandOverride, when non-empty, replaces agent.CtxloomCommand() as
	// the ctxloom-managed [mcp_servers] entry's command (see
	// agent.ResolveMCPCommand) — set ONLY for an isolated-container cell.
	// Empty (the default) preserves the host self-exec-absolute behavior
	// exactly.
	MCPCommandOverride string
}

func (w *CodexHookWriter) getFS() afero.Fs { return agent.GetFS(w.FS) }

// SettingsPath returns "" — codex has NO project-keyed settings file, and this
// empty string is the DECLARED ABSENCE (declared_absence.go) in path form.
//
// codex's settings surface is $CODEX_HOME/config.toml, and that home exists
// only as a PER-SESSION instance (SessionHome) or as the user's own ~/.codex,
// which ctxloom never writes. A harpless caller can name neither, so there is
// no stable path a static `ctxloom profile materialize --backend codex` could
// write that a later run would read. Returning a plausible-looking
// <projectDir>/.codex/config.toml instead — which this did as the S7 interim —
// hands the caller a file nothing reads, dressed as a real surface.
//
// EVERY CALLER MUST TREAT "" AS "THIS ENGINE HAS NO SUCH FILE", never as a
// relative path: filepath.Join(projectDir, "") is projectDir, so a caller that
// forgets ends up statting a DIRECTORY and reporting it unreadable.
// TestSettingsPath_IsTheDeclaredAbsence pins the value.
func (w *CodexHookWriter) SettingsPath(string) string { return "" }

// settingsPathIn joins config.toml under an ALREADY-RESOLVED codex home parent
// — the "virtual project dir" cellScopedCodexHome derives CODEX_HOME from. The
// run path's three axes each resolve their own (an isolation-provided worktree
// home, a container's fresh $HOME, the in-tree state home), so the surfaces
// hand this one a finished directory rather than a project root to relocate.
func (w *CodexHookWriter) settingsPathIn(codexProjectDir string) string {
	return filepath.Join(codexProjectDir, ConfigDirName, ConfigFileName)
}

// AgentsMDFile is the workspace-fixed file codex reads NATIVELY at session
// start for repo-level instructions (see internal/codex/backend.go /
// surfaces.go: codex "natively reads a workspace-fixed AGENTS.md"). ctxloom
// historically did not write it: an unmanaged whole-file write would have
// clobbered hand-authored content the same way claude's CLAUDE.md was.
// Managed-section markers make it safe — see
// agentsMDPath / WriteContext below.
const AgentsMDFile = "AGENTS.md"

// agentsMDPath returns the path to the workspace AGENTS.md file.
func (w *CodexHookWriter) agentsMDPath(projectDir string) string {
	return filepath.Join(projectDir, AgentsMDFile)
}

// WriteContext implements agent.ContextWriter for Codex CLI: it merges the
// assembled context into the ctxloom-managed section of the workspace
// AGENTS.md, which codex reads NATIVELY at session start — no hook required.
// This is ADDITIVE to the existing SessionStart-hook + content-addressed
// cache-file route (contextSurface in surfaces.go, wired through
// configSurface's hooks): that route remains necessary for the RUN/LAUNCH
// path, which delivers a per-invocation content hash out-of-band of any
// workspace-fixed file (see BaseContextProvider.Provide / setupViaCells).
// AGENTS.md gives the STATIC materialize/init path (`ctxloom profile
// materialize`, no active run) a real context surface it never had — that
// path only ever had the assembled Context STRING (SurfaceInputs.Context), not
// resolved Fragment objects, so the fragments-only cache-file route silently
// delivered nothing there. Preserves hand-authored
// content outside the markers byte-for-byte; empty content removes the managed
// section (and the file, when it was wholly ctxloom's).
func (w *CodexHookWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	path := w.agentsMDPath(req.ProjectDir)
	return agent.WriteManagedContext(w.getFS(), path, AgentsMDFile, req.Context, AgentsMDFile)
}

// WriteSettings implements SettingsWriter for Codex CLI and REFUSES: this
// entry point takes a project dir, and codex has no project-keyed settings
// file to write one into (see SettingsPath and declared_absence.go). An error
// rather than a quiet no-op because the caller asked for a WRITE, and a write
// that reports success having produced nothing is this project's signature
// failure.
//
// The real writer is writeSettingsIn, reached only through the config surface,
// which holds an ALREADY-RESOLVED codex home. There is no exported harpless
// twin of it on purpose: every exported spelling of "write codex's settings
// under a project root" is a durable project home regrowing.
func (w *CodexHookWriter) WriteSettings(*wire.HooksConfig, *wire.MCPConfig, map[string]wire.MCPServer, string) error {
	return launchOnlyError("codex has no project-scoped settings file to write")
}

// writeSettingsIn writes hooks + MCP servers (and, when trustAbsPath is
// non-empty, the project-trust pre-seed) into config.toml under an
// ALREADY-RESOLVED codex home parent — see settingsPathIn.
//
// THE TRUST PRE-SEED: `[projects."<trustAbsPath>"] trust_level = "trusted"` is
// appended so codex does not re-prompt for trust the FIRST time it reads a
// config.toml it has never seen before — and, under `codex exec`, does not
// silently proceed untrusted because there is nobody to prompt. That covers
// every home ctxloom itself provisions — the per-session instance, the
// isolation-provided per-run CODEX_HOME, a container cell's fresh $HOME — none
// of which is ever committed, so a machine-specific absolute path baked into
// one is harmless.
//
// docs/trust-model.md, "Engine workspace-trust prompts", is the NORMATIVE
// statement of that decision and its boundary (ctxloom answers only for homes
// it created, only for the directory the run was asked for); internal/codex/
// backend.go's Setup is what fills the value.
func (w *CodexHookWriter) writeSettingsIn(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, codexProjectDir, trustAbsPath string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.settingsPathIn(codexProjectDir)

	if err := fs.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("failed to create .codex directory: %w", err)
	}

	cfg, err := w.load(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing config.toml: %w", err)
	}

	removeManagedHooks(cfg)
	removeManagedMCP(cfg)

	addUnifiedHooks(cfg, hooks.Unified)
	if backendHooks, ok := hooks.Plugins["codex"]; ok {
		addBackendHooks(cfg, backendHooks)
	}
	addMCPServers(cfg, mcp, bundleMCP, w.MCPCommandOverride)
	if trustAbsPath != "" {
		addProjectTrust(cfg, trustAbsPath)
	}

	return w.save(settingsPath, cfg, false)
}

// addProjectTrust sets `[projects."<absPath>"] trust_level = "trusted"` in
// cfg — the EXACT key/section codex itself appends after a user answers its
// interactive trust prompt (live-verified 2026-07-15 against codex-cli
// 0.144.4's own ~/.codex/config.toml). Idempotent: overwrites any existing
// entry for absPath, preserving its other keys.
func addProjectTrust(cfg map[string]any, absPath string) {
	projects := asMap(cfg["projects"])
	if projects == nil {
		projects = map[string]any{}
	}
	entry := asMap(projects[absPath])
	if entry == nil {
		entry = map[string]any{}
	}
	entry["trust_level"] = "trusted"
	projects[absPath] = entry
	cfg["projects"] = projects
}

// load parses config.toml into a generic table, preserving every key. An
// ABSENT file is an empty table (nothing to preserve); an UNPARSEABLE one is
// an error, because every caller writes the returned table straight back and
// a degraded-to-empty parse would destroy the user's config.
func (w *CodexHookWriter) load(path string) (map[string]any, error) {
	cfg := map[string]any{}
	data, err := afero.ReadFile(w.getFS(), path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		// Was: warn and return an EMPTY table. Every caller then wrote that
		// table back, so an unparseable config.toml was silently REPLACED by
		// one containing ctxloom's keys and nothing else — the user's codex
		// configuration destroyed on a success path. "I could not
		// read it" is not "it was empty"; refuse and say so.
		return nil, fmt.Errorf("cannot parse %s (%w) — refusing to write over a config.toml ctxloom could not read; fix or move the file and re-run", path, err)
	}
	return cfg, nil
}

// save marshals the table back to config.toml via an atomic write+backup.
func (w *CodexHookWriter) save(path string, cfg map[string]any, allowEmpty bool) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("failed to marshal config.toml: %w", err)
	}
	// A 0-byte write over a real file is a wipe wearing a success's clothes.
	// RemoveSettings may legitimately empty the file — stripping ctxloom's
	// keys from a config that held nothing else — and says so with
	// allowEmpty; nothing else may. This mirrors (and, via AllowEmptyWrite,
	// now composes with) agent.AtomicWriteFile's own zero-byte refusal guard:
	// this check gives the more specific error naming the existing byte
	// count, AllowEmptyWrite tells the shared guard not to override the
	// decision this function already made.
	if !allowEmpty && len(bytes.TrimSpace(buf.Bytes())) == 0 {
		if existing, rerr := afero.ReadFile(w.getFS(), path); rerr == nil && len(bytes.TrimSpace(existing)) > 0 {
			return fmt.Errorf("refusing to overwrite %s (%d bytes) with an empty config", path, len(existing))
		}
	}
	var opts []agent.WriteFileOption
	if allowEmpty {
		opts = append(opts, agent.AllowEmptyWrite())
	}
	return agent.AtomicWriteFile(w.getFS(), path, buf.Bytes(), ConfigFileName, opts...)
}

// RemoveSettings implements SettingsWriter for Codex CLI and removes NOTHING —
// there is nothing home-keyed under a project to remove, because a static
// install never wrote any (declared_absence.go).
//
// It SAYS SO rather than returning silently. `ctxloom manage hooks uninstall`
// reporting codex among the backends it cleaned, having touched no file, is
// indistinguishable from an uninstall that missed one — and the user's next
// move on that belief is to go hunting for a file, or to delete the wrong
// directory. The note also states whose the pre-relocation <workDir>/.codex is
// (D3: not ctxloom's).
//
// nil, not an error: nothing to remove is not a failure, and an error here
// would make a whole-project uninstall report "partial" for the one backend
// that had nothing to do. The removal that DOES happen — a session's instance,
// which holds a copied credential — is operations.removeSessionInstance's, at
// session end, not this writer's.
func (w *CodexHookWriter) RemoveSettings(projectDir string) error {
	warnNothingToRemove(projectDir)
	return nil
}

// removeSettingsIn is RemoveSettings against an ALREADY-RESOLVED codex home
// parent — the form configSurface's delivery handle reverts through.
func (w *CodexHookWriter) removeSettingsIn(codexProjectDir string) error {
	fs := w.getFS()
	settingsPath := w.settingsPathIn(codexProjectDir)
	exists, err := afero.Exists(fs, settingsPath)
	if err != nil {
		return fmt.Errorf("cannot determine whether %s exists: %w", settingsPath, err)
	}
	if !exists {
		return nil
	}
	cfg, err := w.load(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing config.toml: %w", err)
	}
	removeManagedHooks(cfg)
	removeManagedMCP(cfg)
	return w.save(settingsPath, cfg, true)
}

// Status implements SettingsWriter for Codex CLI and reports the EMPTY status:
// no project-keyed settings file exists to report on (SettingsPath, and
// declared_absence.go for why). `ctxloom manage check` therefore shows codex
// with every surface false, which is the literal truth about the project tree —
// what codex actually reads lives in a per-session instance that exists only
// while a session is running, and in the user's own ~/.codex, which
// `ctxloom doctor` reports (DOCTOR-CHECK-CODEXHOME-n4).
//
// A nil error, not a refusal: a status read is a question, and "this engine
// keeps nothing here" is a complete answer to it. Erroring would make one
// backend with nothing to report blot the whole cross-backend status line.
func (w *CodexHookWriter) Status(string) (agent.SettingsStatus, error) {
	return agent.SettingsStatus{}, nil
}

// --- generic TOML table helpers -------------------------------------------

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asSlice(v any) []any        { s, _ := v.([]any); return s }

// isManagedServer reports whether an mcp_servers entry was installed by ctxloom,
// recognized by its command's executable token (matches bare/path/quoted forms).
func isManagedServer(server map[string]any) bool {
	cmd, _ := server["command"].(string)
	return agent.IsManaged(cmd, "ctxloom")
}

// --- hook removal / addition ----------------------------------------------

// eachHookGroup is the ONE place codex's [hooks.EVENT] array-of-tables descent
// (event -> groups -> hooks[]) is written. It hands prune each group's hook-entry
// slice; prune returns the entries to keep plus whether it changed anything.
//
// A group prune leaves UNCHANGED is passed through byte-for-byte, so a read-only
// caller (one that always reports false) walks without touching cfg at all —
// and, just as importantly, a user-authored group that happens to hold no
// entries is never collected as collateral. Only where prune reports a change
// is the surrounding structure reconciled: a group emptied by prune is dropped,
// an event left with no groups is removed, and an emptied hooks table goes with
// it, which is the shape codex's TOML expects.
//
// The pruning walk and the boolean query share this descent rather than each
// carrying a copy: separate copies disagree — the pruning one deletes
// ctxloom-free empty groups the query never counts (see
// TestManagedHooks_QueryAgreesWithRemoval).
func eachHookGroup(cfg map[string]any, prune func(entries []any) (kept []any, changed bool)) {
	hooks := asMap(cfg["hooks"])
	if hooks == nil {
		return
	}
	touched := false
	for event, groupsRaw := range hooks {
		groups, changed := pruneHookGroups(asSlice(groupsRaw), prune)
		if !changed {
			continue
		}
		touched = true
		if len(groups) > 0 {
			hooks[event] = groups
		} else {
			delete(hooks, event)
		}
	}
	if touched && len(hooks) == 0 {
		delete(cfg, "hooks")
	}
}

// pruneHookGroups applies prune to one event's groups, returning the surviving
// groups and whether any of them changed. A group that is not a table is passed
// through untouched — codex owns that shape, not ctxloom.
func pruneHookGroups(groups []any, prune func([]any) ([]any, bool)) ([]any, bool) {
	var kept []any
	changed := false
	for _, g := range groups {
		gm := asMap(g)
		if gm == nil {
			kept = append(kept, g)
			continue
		}
		entries, groupChanged := prune(asSlice(gm["hooks"]))
		if !groupChanged {
			kept = append(kept, g)
			continue
		}
		changed = true
		if len(entries) > 0 {
			gm["hooks"] = entries
			kept = append(kept, gm)
		}
	}
	return kept, changed
}

// isManagedHookCommand reports whether one hook entry was installed by ctxloom,
// recognized by its command's executable token — the hook-entry counterpart of
// isManagedServer.
func isManagedHookCommand(entry any) bool {
	cmd, _ := asMap(entry)["command"].(string)
	return agent.IsManaged(cmd, "ctxloom")
}

// removeManagedHooks drops ctxloom-managed entries from every [hooks.EVENT]
// group. Entries ctxloom did not write are preserved, and so is any group or
// event ctxloom had nothing in.
func removeManagedHooks(cfg map[string]any) {
	eachHookGroup(cfg, func(entries []any) ([]any, bool) {
		var kept []any
		for _, e := range entries {
			if isManagedHookCommand(e) {
				continue
			}
			kept = append(kept, e)
		}
		return kept, len(kept) != len(entries)
	})
}

// hasManagedHook reports whether any configured hook is ctxloom-managed. It
// rides the same descent as the removal and reports no change, so it is a pure
// read.
func hasManagedHook(cfg map[string]any) bool {
	found := false
	eachHookGroup(cfg, func(entries []any) ([]any, bool) {
		for _, e := range entries {
			if isManagedHookCommand(e) {
				found = true
			}
		}
		return entries, false
	})
	return found
}

// NoSessionEndReason is the one-clause reason Codex has no native event for
// the unified session_end hook — the SAME string used both by
// addUnifiedHooks' route below (to warn once, at write time, when a
// session_end hook is actually configured and would otherwise vanish
// silently) and by internal/lm/backends' registry descriptor (to report the
// identical loss to a caller that never writes settings at all — doctor/
// agent show/acp list, wiring an existing declaration to more surfaces
// rather than hand-maintaining a second copy of the sentence).
const NoSessionEndReason = "codex has no session-end event"

// addUnifiedHooks translates unified hooks to Codex event names and adds them.
// Codex lacks a SessionEnd event, so unified SessionEnd hooks cannot be emitted
// — the route declares that gap explicitly so a configured
// session_end hook is announced as inert instead of vanishing.
func addUnifiedHooks(cfg map[string]any, u wire.UnifiedHooks) {
	agent.RouteUnifiedHooks("codex", []agent.HookRoute{
		{Hooks: u.SessionStart, Event: "SessionStart"},
		{Hooks: u.SessionEnd, Kind: "session_end", Unsupported: NoSessionEndReason},
		{Hooks: u.PreTool, Event: "PreToolUse"},
		{Hooks: u.PostTool, Event: "PostToolUse"},
		{Hooks: u.PreShell, Event: "PreToolUse", DefaultMatcher: "Bash"},
		{Hooks: u.PostFileEdit, Event: "PostToolUse", DefaultMatcher: "Edit|Write"},
	}, func(event string, h wire.Hook) {
		addHook(cfg, event, h)
	})
}

// addBackendHooks adds backend-specific passthrough hooks (already keyed by
// Codex-native event names).
func addBackendHooks(cfg map[string]any, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			addHook(cfg, eventName, h)
		}
	}
}

// addHook appends one command hook to the named event as its own matcher group,
// emitting Codex's [[hooks.EVENT]] / [[hooks.EVENT.hooks]] shape. Timeout is
// carried in seconds (Codex's unit); zero is omitted so Codex applies its default.
func addHook(cfg map[string]any, eventName string, h wire.Hook) {
	hooks := asMap(cfg["hooks"])
	if hooks == nil {
		hooks = map[string]any{}
		cfg["hooks"] = hooks
	}

	// Drop any surviving entry with this exact command AND matcher first.
	// removeManagedHooks only recognizes ctxloom-token commands; hooks ctxloom
	// writes for companion binaries (e.g. `ltk evaluate`) carry no marker and
	// would duplicate on every re-apply. Matching on (command, matcher) keeps
	// user variants untouched and lets the same command coexist under distinct
	// matchers (e.g. an all-tools PreToolUse entry and a Bash-scoped one).
	removeExactCommand(hooks, eventName, h.Command, h.Matcher)

	entry := map[string]any{"type": "command", "command": h.Command}
	if h.Timeout > 0 {
		entry["timeout"] = h.Timeout
	}

	group := map[string]any{"hooks": []any{entry}}
	if h.Matcher != "" {
		group["matcher"] = h.Matcher
	}
	hooks[eventName] = append(asSlice(hooks[eventName]), group)
}

// removeExactCommand drops hook entries under eventName whose command is exactly
// cmd AND whose group matcher is exactly matcher, pruning emptied groups and
// events. Companion-binary hooks carry no durable marker, so identity is the
// (verbatim command, matcher) pair: this dedups true re-applies while letting
// the same command live under different matchers (groups with a non-matching
// matcher are left fully intact).
func removeExactCommand(hooks map[string]any, eventName, cmd, matcher string) {
	var keptGroups []any
	for _, g := range asSlice(hooks[eventName]) {
		gm := asMap(g)
		if gm == nil {
			keptGroups = append(keptGroups, g)
			continue
		}
		gMatcher, _ := gm["matcher"].(string)
		if gMatcher != matcher {
			keptGroups = append(keptGroups, gm)
			continue
		}
		var keptEntries []any
		for _, e := range asSlice(gm["hooks"]) {
			c, _ := asMap(e)["command"].(string)
			if c != cmd {
				keptEntries = append(keptEntries, e)
			}
		}
		if len(keptEntries) > 0 {
			gm["hooks"] = keptEntries
			keptGroups = append(keptGroups, gm)
		}
	}
	if len(keptGroups) > 0 {
		hooks[eventName] = keptGroups
	} else {
		delete(hooks, eventName)
	}
}

// --- MCP removal / addition -----------------------------------------------

// removeManagedMCP drops ctxloom-managed servers from [mcp_servers]: the
// well-known "ctxloom" auto-server and any entry whose command resolves to
// ctxloom. Bundle/unified servers are re-added by name (the map overwrites).
func removeManagedMCP(cfg map[string]any) {
	servers := asMap(cfg["mcp_servers"])
	if servers == nil {
		return
	}
	for name, s := range servers {
		if name == agent.MCPServerName || isManagedServer(asMap(s)) {
			delete(servers, name)
		}
	}
	if len(servers) == 0 {
		delete(cfg, "mcp_servers")
	}
}

// addMCPServers adds MCP servers from config and bundles to [mcp_servers].
// override replaces agent.CtxloomCommand() for the ctxloom-managed entry when
// non-empty (see agent.ResolveMCPCommand) — set ONLY for an isolated-
// container cell.
func addMCPServers(cfg map[string]any, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, override string) {
	servers := asMap(cfg["mcp_servers"])
	if servers == nil {
		servers = map[string]any{}
	}

	// Auto-register ctxloom's own MCP server unless disabled. Command names
	// the self-exec absolute path (agent.CtxloomCommand) so this session's
	// MCP server can never diverge from the binary that materialized it —
	// unless override substitutes the in-container path.
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		servers[agent.MCPServerName] = mcpServerToTOMLEntry(wire.MCPServer{Command: agent.ResolveMCPCommand(override), Args: agent.CtxloomMCPArgs})
	}

	// Profile-bundle servers (loaded first, can be overridden).
	for name, server := range bundleMCP {
		servers[name] = mcpServerToTOMLEntry(server)
	}

	if mcp != nil {
		for name, server := range mcp.Servers {
			servers[name] = mcpServerToTOMLEntry(server)
		}
		for name, server := range mcp.Plugins["codex"] {
			servers[name] = mcpServerToTOMLEntry(server)
		}
	}

	if len(servers) > 0 {
		cfg["mcp_servers"] = servers
	}
}

// mcpServerToTOMLEntry builds a Codex [mcp_servers.NAME] table value
// (command + optional args/env) from a wire.MCPServer. It is the single source
// of truth for the entry shape, shared by addMCPServers and
// MCPRegistrar.Install so the two can never drift.
func mcpServerToTOMLEntry(server wire.MCPServer) map[string]any {
	entry := map[string]any{"command": server.Command}
	if len(server.Args) > 0 {
		anyArgs := make([]any, len(server.Args))
		for i, a := range server.Args {
			anyArgs[i] = a
		}
		entry["args"] = anyArgs
	}
	if len(server.Env) > 0 {
		env := make(map[string]any, len(server.Env))
		for k, v := range server.Env {
			env[k] = v
		}
		entry["env"] = env
	}
	return entry
}
