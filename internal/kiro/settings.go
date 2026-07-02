package kiro

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// kiroDir is the workspace directory Kiro reads configuration from.
const kiroDir = ".kiro"

// kiroMCPLedger records which settings/mcp.json server names are
// ctxloom-managed (one per line). Kiro's tolerance for unknown fields inside
// mcp.json is unverified, so ownership is tracked out-of-file (as with agy)
// rather than via an in-file marker — a renamed/removed server would otherwise
// linger in the config forever.
const kiroMCPLedger = ".ctxloom-mcp-managed"

// steeringFileName is the ctxloom-owned steering file carrying the assembled
// context (front-matter `inclusion: always`, auto-loaded by Kiro every session).
const steeringFileName = "ctxloom-context.md"

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
// WARNING: implemented against Kiro's published docs (agent-config,
// settings/mcp.json, steering, skills), not yet verified live against kiro-cli
// (agent operations are auth-gated). File shapes and the hook exec/decision
// protocol may need adjustment once verified — the same doc-first posture the
// codex writer shipped with.
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
}

func (w *KiroWriter) getFS() afero.Fs { return agent.GetFS(w.FS) }

func (w *KiroWriter) warn(format string, args ...interface{}) { agent.Warn(format, args...) }

func (w *KiroWriter) agentPath(projectDir string) string {
	return filepath.Join(projectDir, kiroDir, "agents", defaultAgentName+".json")
}

func (w *KiroWriter) mcpPath(projectDir string) string {
	return filepath.Join(projectDir, kiroDir, "settings", "mcp.json")
}

func (w *KiroWriter) mcpLedgerPath(projectDir string) string {
	return filepath.Join(projectDir, kiroDir, "settings", kiroMCPLedger)
}

func (w *KiroWriter) steeringPath(projectDir string) string {
	return filepath.Join(projectDir, kiroDir, "steering", steeringFileName)
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

	for _, hook := range u.SessionStart {
		if hook.ContextHash != "" {
			contextHash = hook.ContextHash
			continue
		}
		add(&h.AgentSpawn, hook, "")
	}
	for _, hook := range u.SessionEnd {
		add(&h.Stop, hook, "")
	}
	for _, hook := range u.PreTool {
		add(&h.PreToolUse, hook, "")
	}
	for _, hook := range u.PostTool {
		add(&h.PostToolUse, hook, "")
	}
	for _, hook := range u.PreShell {
		add(&h.PreToolUse, hook, kiroShellMatcher)
	}
	for _, hook := range u.PostFileEdit {
		add(&h.PostToolUse, hook, kiroFileEditMatcher)
	}

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
func (w *KiroWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
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
	return w.mcpFile(projectDir).WriteServers(mcp, bundleMCP)
}

func (w *KiroWriter) writeAgentConfig(projectDir string, h kiroHooks) error {
	a := kiroAgent{
		Name:           defaultAgentName,
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
		return fmt.Errorf("failed to create %s agents directory: %w", kiroDir, err)
	}
	data, err := agent.CanonicalJSON(a)
	if err != nil {
		return fmt.Errorf("failed to marshal kiro agent config: %w", err)
	}
	return agent.AtomicWriteFile(fs, path, data, defaultAgentName+".json")
}

// reconcileSteering writes (or removes, when hash is empty) the ctxloom-owned
// steering file carrying the assembled context.
func (w *KiroWriter) reconcileSteering(projectDir, hash string) error {
	fs := w.getFS()
	path := w.steeringPath(projectDir)

	remove := func() error {
		if exists, _ := afero.Exists(fs, path); exists {
			return fs.Remove(path)
		}
		return nil
	}
	if hash == "" {
		return remove()
	}
	content, err := agent.ReadContextFile(projectDir, hash, agent.WithContextFS(fs))
	if err != nil {
		w.warn("failed to read context file %s: %v - context will not be delivered to kiro", hash, err)
		return remove()
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create %s steering directory: %w", kiroDir, err)
	}
	body := "---\ninclusion: always\n---\n\n" + content + "\n"
	return agent.AtomicWriteFile(fs, path, []byte(body), steeringFileName)
}

// --- MCP (.kiro/settings/mcp.json), preserved + ledger-tracked ---

// kiroMCPServer is the stdio server shape ctxloom writes. Remote (url) servers
// are user-authored and pass through raw.
// mcpFile binds the shared MCP-registry reconciler (agent.MCPFileConfig —
// load/save/ledger/reconcile live there, shared with antigravity) to this
// project's settings/mcp.json + sidecar ledger.
func (w *KiroWriter) mcpFile(projectDir string) agent.MCPFileConfig {
	return agent.MCPFileConfig{
		FS:         w.getFS(),
		Path:       w.mcpPath(projectDir),
		LedgerPath: w.mcpLedgerPath(projectDir),
		Label:      kiroDir + "/settings/mcp.json",
		PluginKey:  "kiro",
		Warn:       w.warn,
	}
}

// RemoveSettings implements SettingsWriter for Kiro CLI: it removes the
// ctxloom-owned agent config and steering file, and drops managed MCP servers
// from settings/mcp.json, leaving absent files absent and user entries intact.
func (w *KiroWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()

	if exists, _ := afero.Exists(fs, w.agentPath(projectDir)); exists {
		if err := fs.Remove(w.agentPath(projectDir)); err != nil {
			return err
		}
	}

	if err := w.mcpFile(projectDir).RemoveServers(); err != nil {
		return err
	}

	return w.reconcileSteering(projectDir, "")
}

// Status implements SettingsWriter for Kiro CLI.
func (w *KiroWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus

	if data, err := afero.ReadFile(fs, w.agentPath(projectDir)); err == nil {
		status.SettingsExists = true
		var a kiroAgent
		if err := json.Unmarshal(data, &a); err == nil && a.Hooks != nil && !a.Hooks.empty() {
			status.HooksPresent = true
		}
	}

	mcpPresent, err := w.mcpFile(projectDir).ManagedPresent()
	if err != nil {
		return status, err
	}
	status.MCPPresent = mcpPresent

	// The steering file is Kiro's stand-in for the SessionStart injection hook
	// other agents carry, so it counts as a managed hook for wired-status.
	if exists, _ := afero.Exists(fs, w.steeringPath(projectDir)); exists {
		status.HooksPresent = true
	}

	return status, nil
}
