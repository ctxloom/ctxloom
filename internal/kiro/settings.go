package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
	return w.writeMCPConfig(projectDir, mcp, bundleMCP)
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
type kiroMCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// kiroMCPFile represents .kiro/settings/mcp.json. Server entries are kept raw so
// fields ctxloom doesn't model (url/headers/oauth/disabled/autoApprove for
// user-authored servers) survive a rewrite byte-for-byte.
type kiroMCPFile struct {
	Servers map[string]json.RawMessage
	Other   map[string]json.RawMessage
}

func (w *KiroWriter) readMCPLedger(projectDir string) []string {
	data, err := afero.ReadFile(w.getFS(), w.mcpLedgerPath(projectDir))
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

func (w *KiroWriter) writeMCPLedger(projectDir string, names []string) error {
	fs := w.getFS()
	path := w.mcpLedgerPath(projectDir)
	if len(names) == 0 {
		if exists, _ := afero.Exists(fs, path); exists {
			return fs.Remove(path)
		}
		return nil
	}
	sort.Strings(names)
	return iox.WriteFileAtomicFs(fs, path, []byte(strings.Join(names, "\n")+"\n"), 0644)
}

func (w *KiroWriter) writeMCPConfig(projectDir string, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) error {
	mcpPath := w.mcpPath(projectDir)
	mf, err := w.loadMCPFile(mcpPath)
	if err != nil {
		return fmt.Errorf("failed to load existing mcp.json: %w", err)
	}

	// Reconcile: drop everything previously managed (the ledger) plus the
	// well-known ctxloom server name for pre-ledger files.
	delete(mf.Servers, agent.MCPServerName)
	for _, name := range w.readMCPLedger(projectDir) {
		delete(mf.Servers, name)
	}

	var managed []string
	add := func(name string, s kiroMCPServer) {
		w.setServer(mf, name, s)
		managed = append(managed, name)
	}

	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		add(agent.MCPServerName, kiroMCPServer{Command: agent.CtxloomBinary, Args: agent.CtxloomMCPArgs})
	}
	for name, server := range bundleMCP {
		add(name, kiroMCPServer{Command: server.Command, Args: server.Args, Env: server.Env})
	}
	if mcp != nil {
		for name, server := range mcp.Servers {
			add(name, kiroMCPServer{Command: server.Command, Args: server.Args, Env: server.Env})
		}
		if backendServers, ok := mcp.Plugins["kiro"]; ok {
			for name, server := range backendServers {
				add(name, kiroMCPServer{Command: server.Command, Args: server.Args, Env: server.Env})
			}
		}
	}

	if err := w.saveMCPFile(mcpPath, mf); err != nil {
		return err
	}
	return w.writeMCPLedger(projectDir, managed)
}

func (w *KiroWriter) setServer(mf *kiroMCPFile, name string, s kiroMCPServer) {
	data, err := json.Marshal(s)
	if err != nil {
		w.warn("failed to marshal MCP server %q: %v", name, err)
		return
	}
	mf.Servers[name] = data
}

func (w *KiroWriter) loadMCPFile(path string) (*kiroMCPFile, error) {
	mf := &kiroMCPFile{
		Servers: make(map[string]json.RawMessage),
		Other:   make(map[string]json.RawMessage),
	}
	data, err := afero.ReadFile(w.getFS(), path)
	if err != nil {
		if os.IsNotExist(err) {
			return mf, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return mf, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		w.warn("failed to parse %s/settings/mcp.json: %v - existing MCP servers may not be preserved", kiroDir, err)
		return mf, nil
	}
	if serversRaw, ok := raw["mcpServers"]; ok {
		if err := json.Unmarshal(serversRaw, &mf.Servers); err != nil {
			w.warn("failed to parse mcpServers in mcp.json: %v - existing MCP servers may not be preserved", err)
		}
		delete(raw, "mcpServers")
	}
	mf.Other = raw
	return mf, nil
}

func (w *KiroWriter) saveMCPFile(path string, mf *kiroMCPFile) error {
	output := make(map[string]interface{})
	for k, v := range mf.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			w.warn("failed to preserve mcp.json field %q: %v", k, err)
			continue
		}
		output[k] = val
	}
	if len(mf.Servers) > 0 {
		servers := make(map[string]interface{}, len(mf.Servers))
		for name, rawServer := range mf.Servers {
			var val interface{}
			if err := json.Unmarshal(rawServer, &val); err != nil {
				w.warn("failed to preserve MCP server %q: %v", name, err)
				continue
			}
			servers[name] = val
		}
		output["mcpServers"] = servers
	}

	if len(output) == 0 {
		if exists, _ := afero.Exists(w.getFS(), path); !exists {
			return nil
		}
	}
	if err := w.getFS().MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create %s settings directory: %w", kiroDir, err)
	}
	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal mcp.json: %w", err)
	}
	return agent.AtomicWriteFile(w.getFS(), path, data, "mcp.json")
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

	mcpPath := w.mcpPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mf, err := w.loadMCPFile(mcpPath)
		if err != nil {
			return fmt.Errorf("failed to load existing mcp.json: %w", err)
		}
		delete(mf.Servers, agent.MCPServerName)
		for _, name := range w.readMCPLedger(projectDir) {
			delete(mf.Servers, name)
		}
		if err := w.saveMCPFile(mcpPath, mf); err != nil {
			return err
		}
	}
	if err := w.writeMCPLedger(projectDir, nil); err != nil {
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

	mcpPath := w.mcpPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mf, err := w.loadMCPFile(mcpPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing mcp.json: %w", err)
		}
		if _, ok := mf.Servers[agent.MCPServerName]; ok {
			status.MCPPresent = true
		}
		for _, name := range w.readMCPLedger(projectDir) {
			if _, ok := mf.Servers[name]; ok {
				status.MCPPresent = true
				break
			}
		}
	}

	// The steering file is Kiro's stand-in for the SessionStart injection hook
	// other agents carry, so it counts as a managed hook for wired-status.
	if exists, _ := afero.Exists(fs, w.steeringPath(projectDir)); exists {
		status.HooksPresent = true
	}

	return status, nil
}
