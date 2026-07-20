package config

// Phase 1 of the config-manager rework (see the config-manager plan in the
// coordinating session): additive read accessors covering every direct field
// read of *Config found outside this package. Config.Load() hands the SAME
// *Config pointer to ~45 call sites; ~40 of them already went through
// existing accessor methods (ProfileDefinition, ResolveLLM, ...), but a large
// remainder (operations/mcp_servers.go, operations/agents.go,
// internal/cli/doctor_cmd.go, internal/lm/backends/*, ...) read exported
// fields directly. Direct field reads are exactly what make the fields
// UNEXPORTABLE later (Phase 3) — every one of them has to become a method
// call first, or unexporting the field is a compile break with nowhere to
// redirect to.
//
// This phase changes NOTHING about how Config is populated or how the six
// known write sites persist — fields stay exported, direct reads/writes
// elsewhere in the tree keep compiling unchanged. Phase 2 migrates readers to
// these accessors, directory by directory; Phase 3 unexports the fields
// (turning "reads through an accessor" from a convention into a compiler-
// enforced fact); Phase 4 replaces the write sites with Manager.Update.
//
// NAMING: an accessor cannot share its field's exact name while the field is
// still exported (Go disallows a field and a method of the same name on one
// type) — so these use this file's own "Get" prefix, matching the
// convention already established elsewhere in this very package
// (GetEditorCommand, GetProfileLoader, GetDefaultLLM, GetCompactionLLM,
// GetCompactionModel, GetDefaultLLMModel, GetCompactionChunkSize). This is
// the PERMANENT naming — Phase 3 does not rename these once the fields free
// up the shorter names, to avoid migrating every Phase 2 call site a second
// time for no functional gain.
//
// COPY POLICY (copy-on-read, one level deep): every accessor that returns a
// map or slice returns a FRESH container — a caller mutating what it gets
// back must never be able to corrupt what every other Load()/Current() holder
// sees. This is not a hypothetical: SetAgent (operations/agents.go) does
// exactly `cfg.Agents[name] = ...` on the shared instance today (armed-chomp,
// tall-nanny) — a getter that handed back the internal map would just move
// that same bug to a different call site wearing an accessor's clothes.
// Elements are copied by value; where an element itself carries a slice/map
// (agents.Agent.Profiles, a Profile's many []string fields, an MCPServer's
// Args/Env, ...), that nested container is cloned too, so the common
// read-then-locally-mutate pattern this codebase actually uses is safe. This
// does not recurse indefinitely — nothing in the tree reaches three levels
// deep into a value obtained this way — and the REAL deep-immutability
// guarantee is Phase 3's unexported fields plus the Phase 4 Manager/Draft
// write path, not these copies. A copy is a defense for well-behaved callers,
// not a security boundary; see TestSnapshot_CannotBeMutatedByReaders (Phase
// 3/4) for the actual enforcement mechanism.

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/agents"
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

func cloneMCPServer(s wire.MCPServer) wire.MCPServer {
	s.Args = cloneStrings(s.Args)
	s.Env = cloneStringMap(s.Env)
	return s
}

func cloneMCPServerMap(m map[string]wire.MCPServer) map[string]wire.MCPServer {
	if m == nil {
		return nil
	}
	out := make(map[string]wire.MCPServer, len(m))
	for k, v := range m {
		out[k] = cloneMCPServer(v)
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
func (c *Config) GetAppPaths() []string { return cloneStrings(c.AppPaths) }

// GetAppDir returns the full path to the resolved .ctxloom directory.
func (c *Config) GetAppDir() string { return c.AppDir }

// GetAppRoot returns the project root (parent of the .ctxloom directory).
func (c *Config) GetAppRoot() string { return c.AppRoot }

// GetSource reports whether this config was resolved from a project or the
// user home directory.
func (c *Config) GetSource() ConfigSource { return c.Source }

// GetVersion returns the config's on-disk schema version.
func (c *Config) GetVersion() int { return c.Version }

// GetWarnings returns a copy of the kind-tagged warnings collected during
// load.
func (c *Config) GetWarnings() []Warning { return cloneWarnings(c.Warnings) }

// GetWorkspace returns the project-wide default workspace axis
// (none | worktree).
func (c *Config) GetWorkspace() string { return c.Workspace }

// GetRuntime returns the project-wide default runtime axis (host |
// container).
func (c *Config) GetRuntime() string { return c.Runtime }

// GetDefaultAgent returns the name of the always-bound default agent (may be
// empty or reference an undefined agent).
func (c *Config) GetDefaultAgent() string { return c.DefaultAgent }

// GetConfiguredAgents returns a copy of the RAW `agents:` config-key map —
// unlike LoadAgents/Agent, it does NOT fold in the .ctxloom/agents/*.yaml
// directory source.
func (c *Config) GetConfiguredAgents() map[string]agents.Agent { return cloneAgentsMap(c.Agents) }

// GetPendingUpgrade returns the PROJECT (or home, when no project layer)
// pending schema upgrade, or nil when the on-disk schema was already
// current.
func (c *Config) GetPendingUpgrade() *upgrade.Pending { return c.PendingUpgrade }

// GetHomePendingUpgrade returns the HOME layer's pending schema upgrade
// (only populated when a project layer also exists), or nil.
func (c *Config) GetHomePendingUpgrade() *upgrade.Pending { return c.HomePendingUpgrade }

// GetMCPConfig returns a copy of the whole MCP configuration block.
func (c *Config) GetMCPConfig() wire.MCPConfig { return cloneMCPConfig(c.MCP) }

// GetMCPServers returns a copy of the unified MCP server map.
func (c *Config) GetMCPServers() map[string]wire.MCPServer { return cloneMCPServerMap(c.MCP.Servers) }

// GetMCPPlugins returns a copy of the per-backend MCP server override map.
func (c *Config) GetMCPPlugins() map[string]map[string]wire.MCPServer {
	return cloneMCPPlugins(c.MCP.Plugins)
}

// GetLMConfig returns a copy of the whole LLM registry + role-default block.
func (c *Config) GetLMConfig() LMConfig { return cloneLMConfig(c.LM) }

// GetLLMEntry returns a copy of one labeled LLM registry entry, and whether
// it exists.
func (c *Config) GetLLMEntry(label string) (LLMConfig, bool) {
	entry, ok := c.LM.Configs[label]
	if !ok {
		return LLMConfig{}, false
	}
	return cloneLLMConfigEntry(entry), true
}

// GetLLMLabels returns every configured LLM registry label, sorted.
func (c *Config) GetLLMLabels() []string {
	out := make([]string, 0, len(c.LM.Configs))
	for label := range c.LM.Configs {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// GetProfileDefinitions returns a copy of the inline `profiles.definitions`
// map.
func (c *Config) GetProfileDefinitions() map[string]Profile {
	return cloneProfilesMap(c.Profiles.Definitions)
}

// GetProfilesConfig returns a copy of the whole `profiles:` block (today just
// the Definitions map, wrapped) — distinct from GetProfileDefinitions, which
// unwraps straight to the map; callers that need the wrapper shape itself
// (e.g. `config get profiles`, which marshals the section verbatim) use this.
func (c *Config) GetProfilesConfig() ProfilesConfig {
	return ProfilesConfig{Definitions: cloneProfilesMap(c.Profiles.Definitions)}
}

// GetSettings returns a copy of the behavioral settings block.
func (c *Config) GetSettings() SettingsConfig { return cloneSettings(c.Settings) }

// GetHooksConfig returns a copy of the whole hooks configuration block.
func (c *Config) GetHooksConfig() wire.HooksConfig { return cloneHooksConfig(c.Hooks) }

// GetSyncConfig returns a copy of the dependency-sync configuration block.
func (c *Config) GetSyncConfig() SyncConfig { return cloneSync(c.Sync) }

// GetEditor returns a copy of the editor configuration block.
func (c *Config) GetEditor() EditorConfig { return cloneEditor(c.Editor) }

// GetUI returns a copy of the interactive-run terminal-layer preferences.
func (c *Config) GetUI() UIConfig { return cloneUIConfig(c.UI) }

// GetIsolationImages returns a copy of the per-backend user-provided agent
// image override map.
func (c *Config) GetIsolationImages() map[string]string { return cloneStringMap(c.IsolationImages) }

// GetIsolationDevcontainerService returns the docker-compose service name
// adopted as the agent image's base, when set.
func (c *Config) GetIsolationDevcontainerService() string { return c.IsolationDevcontainerService }

// GetIsolationEngines returns a copy of the engine fragment selection for the
// shared multi-engine agent image.
func (c *Config) GetIsolationEngines() []string { return cloneStrings(c.IsolationEngines) }
