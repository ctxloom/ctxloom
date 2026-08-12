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
	"reflect"

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

// GetPermissions returns the PROJECT-DIR-SCOPED default launch-time permission
// posture (default | acceptEdits | plan | bypass), as written; empty means the
// project declared none. It is the fourth rung of the resolution chain
// (--permissions flag > agent binding > engine label > THIS > engine built-in),
// so a narrower posture declared anywhere above always wins while a declared
// project posture still beats a silent engine fallback.
//
// The value can only ever have come from THIS project's .ctxloom/config.yaml
// (or an explicit one-invocation --config-set): layerscope scopes the key
// Shared, so a home config or an environment variable carrying it is dropped
// with a warning before the merge. See Config.permissions' own doc.
func (c *Config) GetPermissions() string { return c.permissions }

// GetDelegationConcurrency returns delegation.concurrency: the project-wide
// RESOURCE ceiling on concurrently EXECUTING delegated child turns (0/unset
// means "use the built-in default" — see coord.agentConcurrencyCap and
// DelegationConfig.Concurrency's doc). Renamed/regrouped from
// GetAgentTurnCap.
func (c *Config) GetDelegationConcurrency() int { return c.delegation.Concurrency }

// GetDelegationDepth returns the RESOLVED delegation.depth: the project-wide
// STRUCTURAL ceiling on the delegation tree's depth, with the built-in
// default (DefaultDelegationDepth) already applied when the config leaves it
// unset. This is DELIBERATELY UNLIKE GetDelegationConcurrency, which returns
// the raw 0-means-unset value and leaves resolution to its one consumer
// (coord.resolveTunables): depth's cap must be computed IDENTICALLY by two
// independent processes that never talk to each other — the coordinator
// (server-side "may this run spawn" guard) and a spawned runner (local leaf
// computation, attachRunnerMCP) — so the resolved number has to come from
// one place both can reach. This accessor is that place; coord.agentDepthCap
// is defined in terms of DefaultDelegationDepth for the identical reason.
func (c *Config) GetDelegationDepth() int {
	if c.delegation.Depth > 0 {
		return c.delegation.Depth
	}
	return DefaultDelegationDepth
}

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

// IsLLMUserAuthored reports whether label's llm.configs entry was actually
// declared by the user (in config.yaml, any layer) rather than merged in by
// mergeDefaultConfig's whole-registry fallback for a project that configured
// no LLMs at all (LMConfig's own doc: "not a per-key overlay" — it is a
// stand-in for the ENTIRE registry, not a per-label default). Without this
// distinction, `llm remove claude-code` on a project that never wrote a
// single llm.configs line would see "claude-code" as already present (the
// fallback put it there) and report success while deleting nothing: the
// merged entry was never going to be persisted to begin with (see
// userAuthoredLM, the identical comparison this method exposes per-label).
func (c *Config) IsLLMUserAuthored(label string) bool {
	entry, ok := c.lm.Configs[label]
	if !ok {
		return false
	}
	// lmDefaultOverlay is nil whenever mergeDefaultConfig never ran (the
	// user's llm.configs was non-empty to begin with, so every entry is
	// unambiguously theirs) OR the embedded default failed to parse — either
	// way, nothing in cfg.lm.Configs could have come from the fallback.
	if c.lmDefaultOverlay == nil {
		return true
	}
	def, inOverlay := c.lmDefaultOverlay.Configs[label]
	// Present in the overlay AND byte-identical to it: exactly what the
	// fallback injected, untouched — not user-authored. Absent from the
	// overlay, or present but CHANGED from it (a user override sharing a
	// default's name), is user-authored.
	return !inOverlay || !reflect.DeepEqual(entry, def)
}

// LabelEnv returns the environment a label declares (llm.configs.<label>.env),
// or nil when the label is unknown or declares none.
//
// This exists because forgetting it is a REPEATED defect, not a hypothetical
// one. llm.configs.<label>.env is the documented home for a backend's
// credentials, and a caller that builds a RunStart without forwarding it runs
// the backend unconfigured — which does not error, so it fails silently and
// looks like the model simply answered badly. internal/memory's runDistill
// shipped that way (see CompactionConfig.Env), and internal/operations'
// runTriageCall then reproduced it while explicitly claiming to mirror
// runDistill: it copied the shape before the fix.
//
// Resolving it HERE, from the entry's own body, is what stops the next caller
// repeating it — the alternative pattern (each caller resolves the env and
// passes it in) is exactly what both of those callers failed to do.
//
// The read is generic rather than type-switched per backend: env is an
// ordinary key in the entry's inline Body (mapstructure ",remain"), so this
// needs no knowledge of which backend the label names.
func (c *Config) LabelEnv(label string) map[string]string {
	entry, ok := c.lm.Configs[label]
	if !ok {
		return nil
	}
	raw, ok := entry.Body["env"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	env := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			env[k] = s
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
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
