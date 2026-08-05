package config

// *Config's fields are unexported; every cross-package read goes through a
// Get* accessor in this file. NAMING: the "Get" prefix matches the
// convention already established elsewhere in this package (GetEditorCommand,
// GetProfileLoader, GetDefaultLLM, GetCompactionLLM, GetCompactionModel,
// GetDefaultLLMModel, GetCompactionChunkSize) — Go disallows a field and a
// method of the same name on one type, so a shorter, field-shadowing name
// was never an option.
//
// COPY POLICY (copy-on-read, one level deep): every accessor that returns a
// map or slice returns a FRESH container — a caller mutating what it gets
// back must never be able to corrupt what every other Load() holder sees.
// Elements are copied by value; where an element itself carries a slice/map
// (agents.Agent.Profiles, a Profile's many []string fields, an MCPServer's
// Args/Env, ...), that nested container is cloned too, so the common
// read-then-locally-mutate pattern this codebase actually uses is safe. This
// does not recurse indefinitely — nothing in the tree reaches three levels
// deep into a value obtained this way — and the real immutability guarantee
// is the unexported fields plus the Manager/Draft write path (Manager.Update),
// not these copies. A copy is a defense for well-behaved callers, not a
// security boundary; see TestSnapshot_CannotBeMutatedByReaders for the actual
// enforcement mechanism.

import (
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// --- clone helpers -----------------------------------------------------

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBoolPtr(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func cloneWarnings(w []Warning) []Warning {
	if w == nil {
		return nil
	}
	out := make([]Warning, len(w))
	copy(out, w)
	return out
}

func cloneAgent(a agents.Agent) agents.Agent {
	a.Profiles = cloneStrings(a.Profiles)
	if a.Escalation != nil {
		out := make([]agents.EscalationRung, len(a.Escalation))
		for i, r := range a.Escalation {
			r.Kinds = cloneStrings(r.Kinds)
			out[i] = r
		}
		a.Escalation = out
	}
	return a
}

func cloneAgentsMap(m map[string]agents.Agent) map[string]agents.Agent {
	if m == nil {
		return nil
	}
	out := make(map[string]agents.Agent, len(m))
	for k, v := range m {
		out[k] = cloneAgent(v)
	}
	return out
}

func cloneMCPServerMap(m map[string]wire.MCPServer) map[string]wire.MCPServer {
	if m == nil {
		return nil
	}
	out := make(map[string]wire.MCPServer, len(m))
	for k, v := range m {
		out[k] = wire.CloneMCPServer(v)
	}
	return out
}

func cloneMCPPlugins(m map[string]map[string]wire.MCPServer) map[string]map[string]wire.MCPServer {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]wire.MCPServer, len(m))
	for k, v := range m {
		out[k] = cloneMCPServerMap(v)
	}
	return out
}

func cloneMCPConfig(m wire.MCPConfig) wire.MCPConfig {
	m.AutoRegisterCtxloom = cloneBoolPtr(m.AutoRegisterCtxloom)
	m.Servers = cloneMCPServerMap(m.Servers)
	m.Plugins = cloneMCPPlugins(m.Plugins)
	return m
}

func cloneLLMConfigEntry(l LLMConfig) LLMConfig {
	l.Body = deepCopyBody(l.Body)
	return l
}

func cloneLMConfig(l LMConfig) LMConfig {
	if l.Configs != nil {
		out := make(map[string]LLMConfig, len(l.Configs))
		for k, v := range l.Configs {
			out[k] = cloneLLMConfigEntry(v)
		}
		l.Configs = out
	}
	return l
}

func cloneHooksList(hs []wire.Hook) []wire.Hook {
	if hs == nil {
		return nil
	}
	out := make([]wire.Hook, len(hs))
	copy(out, hs)
	return out
}

func cloneUnifiedHooks(u wire.UnifiedHooks) wire.UnifiedHooks {
	u.PreTool = cloneHooksList(u.PreTool)
	u.PostTool = cloneHooksList(u.PostTool)
	u.SessionStart = cloneHooksList(u.SessionStart)
	u.SessionEnd = cloneHooksList(u.SessionEnd)
	u.PreShell = cloneHooksList(u.PreShell)
	u.PostFileEdit = cloneHooksList(u.PostFileEdit)
	return u
}

func cloneBackendHooks(b wire.BackendHooks) wire.BackendHooks {
	if b == nil {
		return nil
	}
	out := make(wire.BackendHooks, len(b))
	for k, v := range b {
		out[k] = cloneHooksList(v)
	}
	return out
}

func cloneHooksConfig(h wire.HooksConfig) wire.HooksConfig {
	h.Unified = cloneUnifiedHooks(h.Unified)
	if h.Plugins != nil {
		out := make(map[string]wire.BackendHooks, len(h.Plugins))
		for k, v := range h.Plugins {
			out[k] = cloneBackendHooks(v)
		}
		h.Plugins = out
	}
	return h
}

func cloneProfile(p Profile) Profile {
	p.Parents = cloneStrings(p.Parents)
	p.Tags = cloneStrings(p.Tags)
	p.SelectTags = cloneStrings(p.SelectTags)
	p.Bundles = cloneStrings(p.Bundles)
	p.BundleItems = cloneStrings(p.BundleItems)
	if p.Fragments != nil {
		out := make([]FragmentRef, len(p.Fragments))
		copy(out, p.Fragments)
		p.Fragments = out
	}
	p.Commands = cloneStrings(p.Commands)
	p.Skills = cloneStrings(p.Skills)
	p.Variables = cloneStringMap(p.Variables)
	p.Hooks = cloneHooksConfig(p.Hooks)
	p.MCP = cloneMCPConfig(p.MCP)
	p.ExcludeFragments = cloneStrings(p.ExcludeFragments)
	p.ExcludeMCP = cloneStrings(p.ExcludeMCP)
	p.DenyTools = cloneStrings(p.DenyTools)
	return p
}

func cloneProfilesMap(m map[string]Profile) map[string]Profile {
	if m == nil {
		return nil
	}
	out := make(map[string]Profile, len(m))
	for k, v := range m {
		out[k] = cloneProfile(v)
	}
	return out
}

func cloneSettings(s SettingsConfig) SettingsConfig {
	s.UseDistilled = cloneBoolPtr(s.UseDistilled)
	s.Statusline = cloneBoolPtr(s.Statusline)
	if s.Sign != nil {
		sc := *s.Sign
		s.Sign = &sc
	}
	return s
}

func cloneSync(s SyncConfig) SyncConfig {
	s.AutoSync = cloneBoolPtr(s.AutoSync)
	return s
}

func cloneUIConfig(u UIConfig) UIConfig {
	u.Surround = cloneBoolPtr(u.Surround)
	return u
}

func cloneEditor(e EditorConfig) EditorConfig {
	e.Args = cloneStrings(e.Args)
	return e
}

// --- accessors -----------------------------------------------------------

// GetAppPaths returns a copy of the resolved .ctxloom directory path(s) (at
// most one today).
func (c *Config) GetAppPaths() []string { return cloneStrings(c.appPaths) }

// GetAppDir returns the full path to the resolved .ctxloom directory.
func (c *Config) GetAppDir() string { return c.appDir }

// GetAppRoot returns the project root (parent of the .ctxloom directory).
func (c *Config) GetAppRoot() string { return c.appRoot }

// GetWarnings returns a copy of the kind-tagged warnings collected during
// load.
func (c *Config) GetWarnings() []Warning { return cloneWarnings(c.warnings) }

// GetWorkspace returns the project-wide default workspace axis
// (none | worktree).
func (c *Config) GetWorkspace() string { return c.workspace }

// GetDirtyTreeHandler returns the project-wide default for what a delegated
// agent_run spawn does when it resolves to worktree isolation while the
// parent tree is dirty (commit | copy | stale | fail). Empty means "commit"
// (see operations.defaultDirtyTreeHandler).
func (c *Config) GetDirtyTreeHandler() string { return c.dirtyTreeHandler }

// GetRuntime returns the project-wide default runtime axis (host |
// container).
func (c *Config) GetRuntime() string { return c.runtime }

// GetAgentTurnCap returns the project-wide RESOURCE ceiling on concurrently
// EXECUTING delegated child turns (0/unset means "use the built-in
// default" — see coord.agentTurnCap and Config.agentTurnCap's doc).
func (c *Config) GetAgentTurnCap() int { return c.agentTurnCap }

// GetDefaultAgent returns the name of the always-bound default agent (may be
// empty or reference an undefined agent).
func (c *Config) GetDefaultAgent() string { return c.defaultAgent }

// GetConfiguredAgents returns a copy of the RAW `agents:` config-key map —
// unlike LoadAgents/Agent, it does NOT fold in the .ctxloom/agents/*.yaml
// directory source.
func (c *Config) GetConfiguredAgents() map[string]agents.Agent { return cloneAgentsMap(c.agents) }

// GetPendingUpgrade returns the PROJECT (or home, when no project layer)
// pending schema upgrade, or nil when the on-disk schema was already
// current.
func (c *Config) GetPendingUpgrade() *upgrade.Pending { return c.pendingUpgrade }

// GetHomePendingUpgrade returns the HOME layer's pending schema upgrade
// (only populated when a project layer also exists), or nil.
func (c *Config) GetHomePendingUpgrade() *upgrade.Pending { return c.homePendingUpgrade }

// GetMCPConfig returns a copy of the whole MCP configuration block.
func (c *Config) GetMCPConfig() wire.MCPConfig { return cloneMCPConfig(c.mcp) }

// GetMCPServers returns a copy of the unified MCP server map.
func (c *Config) GetMCPServers() map[string]wire.MCPServer { return cloneMCPServerMap(c.mcp.Servers) }

// GetMCPPlugins returns a copy of the per-backend MCP server override map.
func (c *Config) GetMCPPlugins() map[string]map[string]wire.MCPServer {
	return cloneMCPPlugins(c.mcp.Plugins)
}

// GetLMConfig returns a copy of the whole LLM registry + role-default block.
func (c *Config) GetLMConfig() LMConfig { return cloneLMConfig(c.lm) }

// GetLLMEntry returns a copy of one labeled LLM registry entry, and whether
// it exists.
func (c *Config) GetLLMEntry(label string) (LLMConfig, bool) {
	entry, ok := c.lm.Configs[label]
	if !ok {
		return LLMConfig{}, false
	}
	return cloneLLMConfigEntry(entry), true
}

// GetLLMLabels returns every configured LLM registry label, sorted.
func (c *Config) GetLLMLabels() []string {
	return collections.SortedKeys(c.lm.Configs)
}

// GetProfileDefinitions returns a copy of the inline `profiles.definitions`
// map.
func (c *Config) GetProfileDefinitions() map[string]Profile {
	return cloneProfilesMap(c.profiles.Definitions)
}

// GetProfilesConfig returns a copy of the whole `profiles:` block (today just
// the Definitions map, wrapped) — distinct from GetProfileDefinitions, which
// unwraps straight to the map; callers that need the wrapper shape itself
// (e.g. `config get profiles`, which marshals the section verbatim) use this.
func (c *Config) GetProfilesConfig() ProfilesConfig {
	return ProfilesConfig{Definitions: cloneProfilesMap(c.profiles.Definitions)}
}

// GetSettings returns a copy of the behavioral settings block.
func (c *Config) GetSettings() SettingsConfig { return cloneSettings(c.settings) }

// GetHooksConfig returns a copy of the whole hooks configuration block.
func (c *Config) GetHooksConfig() wire.HooksConfig { return cloneHooksConfig(c.hooks) }

// GetSyncConfig returns a copy of the dependency-sync configuration block.
func (c *Config) GetSyncConfig() SyncConfig { return cloneSync(c.sync) }

// GetIsolationDevcontainerService returns the docker-compose service name
// adopted as the agent image's base, when set.
func (c *Config) GetIsolationDevcontainerService() string { return c.isolationDevcontainerService }

// GetIsolationEngines returns a copy of the engine fragment selection for the
// shared multi-engine agent image.
func (c *Config) GetIsolationEngines() []string { return cloneStrings(c.isolationEngines) }
