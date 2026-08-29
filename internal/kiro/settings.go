package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// ConfigDirName is the workspace directory Kiro reads configuration from.
// Exported (renamed from the package-private kiroDir) so tests/arch's
// engine-layout gate can check internal/lm/isolation's kiroOverlayDirs/
// credentialSeedSpecs literals against this package's own fact —
// isolation cannot import this package in production (kiro -> internal/acp
// -> internal/lm/isolation is a real cycle), so its copy of ".kiro" stays a
// literal there.
const ConfigDirName = ".kiro"

// steeringFileName is the ctxloom-owned steering file carrying the assembled
// context (front-matter `inclusion: always`, auto-loaded by Kiro every session).
const steeringFileName = "ctxloom-context.md"

// steeringFrontMatter is the frame writeSteering wraps the assembled context
// in. It is a CONSTANT rather than a literal spelled at each end because the
// read side (contextSurface.State) has to strip exactly what the write side
// added: two independent spellings would drift and report every delivered
// steering file as stale by a two-line diff the user cannot act on.
const steeringFrontMatter = "---\ninclusion: always\n---\n\n"

// kiroSkillsGlob is the agent `resources` entry that surfaces ctxloom-written
// skills (agentskills SKILL.md files under .kiro/skills/) to the agent.
const kiroSkillsGlob = "skill://.kiro/skills/**/SKILL.md"

// Kiro tool-name matchers for the scoped unified hook events.
const (
	kiroShellMatcher    = "execute_bash"
	kiroFileEditMatcher = "fs_write"
)

// NewWriter constructs the Kiro CLI settings writer.
//
// LIVE-VERIFIED for the paths J000400's @live "Kiro" row exercises: kiro-cli
// resolves the materialized WORKSPACE .kiro/agents/<name>.json over any
// global ~/.kiro/agents copy (its own "Agent conflict ... Using workspace
// version" precedence, confirmed via `kiro-cli agent list`), the agentSpawn
// hook fires on session start, and the steering file's assembled context is
// read by a live turn. NOT exercised live: preToolUse/postToolUse actually
// firing (no scenario has driven a tool call yet) and mcp.json's tolerance
// for unknown fields — same doc-first posture as before for those two.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &KiroWriter{FS: o.FS}
}

// KiroWriter writes ctxloom's managed Kiro config: the ctxloom agent
// (.kiro/agents/<name>.json — hooks + skill resources + includeMcpJson), the
// MCP servers (.kiro/settings/mcp.json), and the assembled context
// (.kiro/steering/ctxloom-context.md). The agent config and steering file are
// ctxloom-owned dedicated files (written wholesale); mcp.json is shared with the
// user (preserved, managed set tracked via the ledger).
type KiroWriter struct {
	FS afero.Fs
	// mcpCommandOverride, when non-empty, replaces agent.CtxloomCommand() as
	// the ctxloom-managed .kiro/settings/mcp.json entry's command (see
	// agent.ResolveMCPCommand) — set ONLY for an isolated-container cell.
	// Empty (the default) preserves the host self-exec-absolute behavior
	// exactly.
	mcpCommandOverride string
	// agentName, when non-empty, replaces defaultAgentName as BOTH the
	// materialized agent JSON's file name (.kiro/agents/<name>.json) and its
	// own "name" field (these two used to disagree — writeAgentConfig always
	// used defaultAgentName while `--agent` launched with the configured
	// KiroConfig.Agent override, a broken launch). Empty (the default)
	// preserves today's "ctxloom" behavior exactly.
	agentName string
}

func (w *KiroWriter) getFS() afero.Fs { return agent.GetFS(w.FS) }

func (w *KiroWriter) warn(format string, args ...interface{}) { agent.Warn(format, args...) }

// resolvedAgentName returns the configured agent-name override, or
// defaultAgentName ("ctxloom") when none was set.
func (w *KiroWriter) resolvedAgentName() string {
	if w.agentName != "" {
		return w.agentName
	}
	return defaultAgentName
}

func (w *KiroWriter) agentPath(projectDir string) string {
	return filepath.Join(projectDir, ConfigDirName, "agents", w.resolvedAgentName()+".json")
}

func (w *KiroWriter) mcpPath(projectDir string) string {
	return filepath.Join(projectDir, ConfigDirName, "settings", "mcp.json")
}

// mcpLedgerDir is the directory holding the shared managed-content marker for
// this project's settings/mcp.json. The marker's NAME is owned by
// internal/shared/ledger, not by this engine.
func (w *KiroWriter) mcpLedgerDir(projectDir string) string {
	return filepath.Join(projectDir, ConfigDirName, "settings")
}

func (w *KiroWriter) steeringPath(projectDir string) string {
	return filepath.Join(projectDir, ConfigDirName, "steering", steeringFileName)
}

// --- agent-JSON hooks (Kiro CLI hooks live inside the agent config) ---

type kiroHook struct {
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
}

type kiroHooks struct {
	AgentSpawn  []kiroHook `json:"agentSpawn,omitempty"`
	PreToolUse  []kiroHook `json:"preToolUse,omitempty"`
	PostToolUse []kiroHook `json:"postToolUse,omitempty"`
	Stop        []kiroHook `json:"stop,omitempty"`
}

func (h kiroHooks) empty() bool {
	return len(h.AgentSpawn)+len(h.PreToolUse)+len(h.PostToolUse)+len(h.Stop) == 0
}

// kiroAgent is the ctxloom-managed Kiro custom agent (.kiro/agents/<name>.json).
// Entirely ctxloom-owned, so written wholesale; its name matches the --agent the
// backend selects. Model is not pinned here (buildArgs passes --model), and
// includeMcpJson pulls the managed servers from settings/mcp.json.
type kiroAgent struct {
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Resources      []string   `json:"resources,omitempty"`
	IncludeMCPJSON bool       `json:"includeMcpJson"`
	Hooks          *kiroHooks `json:"hooks,omitempty"`
}

// clearMatchers returns a copy of hooks with every matcher cleared, warning
// once if it actually dropped one. Kiro's `stop` has no tool to match, so a
// matcher written there would be silently inert; saying so once is cheaper than
// a user wondering why their scoped turn-end hook fires on everything.
func clearMatchers(hooks []wire.Hook) []wire.Hook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]wire.Hook, 0, len(hooks))
	dropped := 0
	for _, hook := range hooks {
		if hook.Matcher != "" {
			dropped++
			hook.Matcher = ""
		}
		out = append(out, hook)
	}
	if dropped > 0 {
		clidiag.WarnOnce("ctxloom", "kiro: %d unified turn_end hook(s) declare a matcher, which kiro's stop event has nothing to match against — the matcher is dropped", dropped)
	}
	return out
}

// NoSessionEndReason is the one-clause reason Kiro has no native event for the
// unified session_end hook. Kiro's only turn-boundary event is `stop`, which
// fires once per TURN; it has no session-teardown trigger at all.
//
// This used to be where unified session_end hooks were written, which made one
// config.yaml fire once per session on claude-code and once per turn on kiro,
// with no warning either way. The route below declares the gap instead, and
// turn_end — the event that genuinely means "the agent finished a turn" — owns
// `stop` now.
//
// Same double duty as codex.NoSessionEndReason: the write-time route here and
// internal/lm/backends' registry descriptor (unsupportedHookKinds) read the
// SAME string, so a caller that never writes settings reports the identical
// sentence without a second hand-maintained copy.
const NoSessionEndReason = "kiro has no session-end event"

// mapHooks translates ctxloom's unified hooks into Kiro's agent-JSON hook block
// and returns the context hash when a context-injection hook was present (that
// one is diverted to the steering file, since Kiro reads steering rather than
// firing a SessionStart hook for context).
func (w *KiroWriter) mapHooks(u wire.UnifiedHooks, plugins wire.BackendHooks) (contextHash string, h kiroHooks) {
	add := func(dst *[]kiroHook, hook wire.Hook, defaultMatcher string) {
		if hook.Type != "" && hook.Type != "command" {
			w.warn("skipping kiro hook of type %q: kiro only supports command hooks", hook.Type)
			return
		}
		matcher := hook.Matcher
		if matcher == "" {
			matcher = defaultMatcher
		}
		*dst = append(*dst, kiroHook{Matcher: matcher, Command: hook.Command})
	}

	// The context-injection hook is diverted before routing — it is the
	// per-agent special handling agent.HookRoute's doc means by "handled by the
	// agent before routing and simply omitted from its route table".
	sessionStart := make([]wire.Hook, 0, len(u.SessionStart))
	for _, hook := range u.SessionStart {
		if hook.ContextHash != "" {
			contextHash = hook.ContextHash
			continue
		}
		sessionStart = append(sessionStart, hook)
	}

	agent.RouteUnifiedHooks("kiro", []agent.HookRoute{
		{Hooks: sessionStart, Event: "agentSpawn"},
		{Hooks: u.SessionEnd, Kind: "session_end", Unsupported: NoSessionEndReason},
		// `stop` takes no matcher — there is no tool to match against at a
		// turn boundary — so the route declares no default and any matcher the
		// user wrote is cleared before the hook is written.
		{Hooks: clearMatchers(u.TurnEnd), Event: "stop"},
		{Hooks: u.PreTool, Event: "preToolUse"},
		{Hooks: u.PostTool, Event: "postToolUse"},
		{Hooks: u.PreShell, Event: "preToolUse", DefaultMatcher: kiroShellMatcher},
		{Hooks: u.PostFileEdit, Event: "postToolUse", DefaultMatcher: kiroFileEditMatcher},
	}, func(event string, hook wire.Hook) {
		switch event {
		case "agentSpawn":
			add(&h.AgentSpawn, hook, "")
		case "preToolUse":
			add(&h.PreToolUse, hook, "")
		case "postToolUse":
			add(&h.PostToolUse, hook, "")
		case "stop":
			add(&h.Stop, hook, "")
		}
	})

	for event, hooks := range plugins {
		for _, hook := range hooks {
			switch event {
			case "agentSpawn", "SessionStart":
				add(&h.AgentSpawn, hook, "")
			case "preToolUse", "PreToolUse":
				add(&h.PreToolUse, hook, "")
			case "postToolUse", "PostToolUse":
				add(&h.PostToolUse, hook, "")
			case "stop", "Stop", "SessionEnd":
				add(&h.Stop, hook, "")
			default:
				w.warn("skipping kiro passthrough hook for unknown event %q", event)
			}
		}
	}
	return contextHash, h
}

// WriteSettings implements SettingsWriter for Kiro CLI.
//
// This method has no PRODUCTION call site — kiro's live settings write rides
// the surfaces x cells seam (settingsSurface.Deliver in surfaces.go), and only
// tests reach this one. The obvious remedy — drop WriteSettings from
// agent.SettingsWriter, then delete this implementation — does NOT hold:
//
//   - opencode's configSurface.Deliver (opencode/surfaces.go) calls its OWN
//     concrete WriteSettings on every delivery, so the method is live
//     repo-wide. Pinned by TestConfigSurface_IsTheOneProductionWriteSettingsCaller.
//   - internal/lm/conformance drives WriteSettings through the INTERFACE for
//     claude/codex; removing it from agent.SettingsWriter deletes
//     conformance assertions, which is a wider change than a deletion.
//   - kiro must satisfy agent.SettingsWriter regardless: uninstall.go reaches it
//     via GetSettingsWriter for RemoveSettings and Status, both live.
//
// So the honest scope is "kiro's body is only test-exercised", not "the method
// is dead" — and shrinking it needs an interface split (write vs
// remove/status) that only the human should decide. The cascade behind it
// (reconcileSteering's hash-reading arm, mapHooks' contextHash return) is
// unreachable in production for the same reason and is left in place with it.
func (w *KiroWriter) WriteSettings(hooks *wire.HooksConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}
	contextHash, agentHooks := w.mapHooks(hooks.Unified, hooks.Plugins["kiro"])

	if err := w.writeAgentConfig(projectDir, agentHooks); err != nil {
		return err
	}
	if err := w.reconcileSteering(projectDir, contextHash); err != nil {
		return err
	}
	return w.mcpFile(projectDir).WriteServers(bundleMCP)
}

func (w *KiroWriter) writeAgentConfig(projectDir string, h kiroHooks) error {
	name := w.resolvedAgentName()
	a := kiroAgent{
		Name:           name,
		Description:    "ctxloom-managed agent (context via steering, MCP via settings/mcp.json).",
		Resources:      []string{kiroSkillsGlob},
		IncludeMCPJSON: true,
	}
	if !h.empty() {
		a.Hooks = &h
	}
	fs := w.getFS()
	path := w.agentPath(projectDir)
	if err := fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create %s agents directory: %w", ConfigDirName, err)
	}
	data, err := agent.CanonicalJSON(a)
	if err != nil {
		return fmt.Errorf("failed to marshal kiro agent config: %w", err)
	}
	return agent.AtomicWriteFile(fs, path, data, name+".json")
}

// WriteContext implements agent.ContextWriter for Kiro: it writes the assembled
// context (req.Context) into the ctxloom-owned steering file, which Kiro
// auto-loads every session (it fires no SessionStart hook for context). Empty
// content removes the steering file.
func (w *KiroWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	return w.writeSteering(req.ProjectDir, req.Context)
}

// reconcileSteering writes (or removes, when hash is empty) the ctxloom-owned
// steering file carrying the assembled context. The content is read from the
// content-addressed context file, then handed to the shared writeSteering core.
//
// An UNRESOLVABLE non-empty hash is a hard error. Empty content is how the
// caller says "deliver no context", and writeSteering acts on that by REMOVING
// the steering file kiro auto-loads — so downgrading a failed read to "" both
// launched the session with zero context bytes and destroyed the last good
// delivery, reporting success either way. A hash is an assertion that context
// exists; failing to resolve it means we could not determine what to deliver,
// which is not the same as being asked to deliver nothing. hash == "" remains
// the legitimate nothing-to-do (it is also how teardown removes the file).
func (w *KiroWriter) reconcileSteering(projectDir, hash string) error {
	content := ""
	if hash != "" {
		var err error
		content, err = agent.ReadContextFile(projectDir, hash, agent.WithContextFS(w.getFS()))
		if err != nil {
			return fmt.Errorf("kiro context delivery: cannot resolve context %s — refusing to launch with no context (and leaving any previous steering file intact): %w", hash, err)
		}
	}
	_, err := w.writeSteering(projectDir, content)
	return err
}

// writeSteering is the steering-file core shared by reconcileSteering
// (hash-addressed) and WriteContext (string-addressed). Non-empty content is
// written with the `inclusion: always` front-matter; empty content removes the
// file. It reports the workspace-relative path written or removed.
// steeringRel is the steering file's path relative to the project dir — the
// one spelling both the write report and the currency read name it by.
func steeringRel() string {
	return filepath.Join(ConfigDirName, "steering", steeringFileName)
}

func (w *KiroWriter) writeSteering(projectDir, content string) (agent.ContextReport, error) {
	fs := w.getFS()
	path := w.steeringPath(projectDir)
	rel := steeringRel()

	if content == "" {
		exists, err := afero.Exists(fs, path)
		if err != nil {
			return agent.ContextReport{}, fmt.Errorf("failed to check %s: %w", path, err)
		}
		if exists {
			if err := fs.Remove(path); err != nil {
				return agent.ContextReport{}, err
			}
		}
		return agent.ContextReport{Removed: []string{rel}}, nil
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return agent.ContextReport{}, fmt.Errorf("failed to create %s steering directory: %w", ConfigDirName, err)
	}
	body := steeringFrontMatter + content + "\n"
	if err := agent.AtomicWriteFile(fs, path, []byte(body), steeringFileName); err != nil {
		return agent.ContextReport{}, err
	}
	return agent.ContextReport{Wrote: []string{rel}}, nil
}

// --- MCP (.kiro/settings/mcp.json), preserved + ledger-tracked ---

// kiroMCPServer is the stdio server shape ctxloom writes. Remote (url) servers
// are user-authored and pass through raw.
// mcpFile binds the shared MCP-registry reconciler (agent.MCPFileConfig —
// load/save/ledger/reconcile live there) to this
// project's settings/mcp.json + sidecar ledger.
func (w *KiroWriter) mcpFile(projectDir string) agent.MCPFileConfig {
	return agent.MCPFileConfig{
		FS:              w.getFS(),
		Path:            w.mcpPath(projectDir),
		LedgerDir:       w.mcpLedgerDir(projectDir),
		Label:           ConfigDirName + "/settings/mcp.json",
		Warn:            w.warn,
		CommandOverride: w.mcpCommandOverride,
	}
}

// RemoveSettings implements SettingsWriter for Kiro CLI: it removes the
// ctxloom-owned agent config and steering file, and drops managed MCP servers
// from settings/mcp.json, leaving absent files absent and user entries intact.
func (w *KiroWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()

	agentPath := w.agentPath(projectDir)
	exists, err := afero.Exists(fs, agentPath)
	if err != nil {
		return fmt.Errorf("failed to check %s: %w", agentPath, err)
	}
	if exists {
		if err := fs.Remove(agentPath); err != nil {
			return err
		}
	}

	if err := w.mcpFile(projectDir).RemoveServers(); err != nil {
		return err
	}

	return w.reconcileSteering(projectDir, "")
}

// Status implements SettingsWriter for Kiro CLI.
//
// A reported status is never a guess (the same invariant codex's
// Status states): an ABSENT agent config or steering file is the
// legitimate "not configured" answer, but a file that exists and cannot be read
// or parsed means the answer is UNKNOWN — which is a different fact from "no
// managed hooks are wired" and must not be reported as one.
func (w *KiroWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus

	agentPath := w.agentPath(projectDir)
	data, err := afero.ReadFile(fs, agentPath)
	switch {
	case err == nil:
		status.SettingsExists = true
		var a kiroAgent
		if err := json.Unmarshal(data, &a); err != nil {
			return status, fmt.Errorf("failed to parse existing %s: %w", agentPath, err)
		}
		if a.Hooks != nil && !a.Hooks.empty() {
			status.HooksPresent = true
		}
	case !os.IsNotExist(err):
		return status, fmt.Errorf("failed to read %s: %w", agentPath, err)
	}

	mcpPresent, err := w.mcpFile(projectDir).ManagedPresent()
	if err != nil {
		return status, err
	}
	status.MCPPresent = mcpPresent

	// The steering file is Kiro's stand-in for the SessionStart injection hook
	// other agents carry, so it counts as a managed hook for wired-status.
	steeringPath := w.steeringPath(projectDir)
	exists, err := afero.Exists(fs, steeringPath)
	if err != nil {
		return status, fmt.Errorf("failed to check %s: %w", steeringPath, err)
	}
	if exists {
		status.HooksPresent = true
	}

	return status, nil
}
