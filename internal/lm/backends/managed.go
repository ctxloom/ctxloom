package backends

import (
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// This file is the HOST side of the setup seam: ctxloom owns config and bundles
// here, resolves them into the wire-typed agent.ManagedConfig, and ships that to
// the backend over RunStart. The agent's Setup consumes only the result
// (BaseLifecycle.MergeManaged), so the launch backends never import
// config/bundles. The assembly that used to run plugin-side in each agent's
// Setup (config.Load + LoadPrompts + MergeConfigHooks) lives here now.

// AssembleManagedConfig resolves the host-side setup payload for one target
// backend: the slash-command exports (mapped to that backend's enablement +
// metadata), the config+default-profile+bundle hook set WITHOUT context-injection
// (the agent appends that itself from its plugin-side context hash), the merged
// config+default-profile MCP servers, and whether ctxloom manages the statusline.
//
// Fault tolerant per CLAUDE.md: a config load failure yields a nil payload — the
// agent's Setup then writes an empty managed set rather than blocking launch.
func AssembleManagedConfig(backendName, workDir string) *agent.ManagedConfig {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return &agent.ManagedConfig{
		Prompts:          commandExportsFor(backendName, LoadPromptExports(cfg)),
		Hooks:            AssembleManagedHooks(cfg, workDir, ""),
		MCP:              assembleManagedMCP(cfg),
		ManageStatusline: cfg.Settings.ShouldManageStatusline(),
	}
}

// commandExportsFor maps loaded bundle content to the named backend's command
// exports (resolving that backend's per-prompt enablement + metadata), or nil
// for a backend without slash-command export. Reuses the same per-backend
// mappers as WriteCommandFilesFor so the two paths can't diverge.
func commandExportsFor(backendName string, prompts []*bundles.LoadedContent) []agent.CommandExport {
	switch backendName {
	case "claude-code":
		return claudeExports(prompts)
	case "gemini":
		return geminiExports(prompts)
	case "codex":
		return codexExports(prompts)
	default:
		return nil
	}
}

// assembleManagedMCP builds the merged MCP server set: config-level servers then
// each default profile's servers (later wins). This is the MCP half of the old
// BaseLifecycle.MergeConfigHooks, lifted host-side now that ctxloom owns config
// resolution.
func assembleManagedMCP(cfg *config.Config) *wire.MCPConfig {
	mcp := &wire.MCPConfig{
		Servers: make(map[string]wire.MCPServer),
		Plugins: make(map[string]map[string]wire.MCPServer),
	}
	if cfg == nil {
		return mcp
	}
	wire.MergeMCPConfig(mcp, &cfg.MCP)
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName)
		if err != nil {
			continue
		}
		wire.MergeMCPConfig(mcp, &resolved.MCP)
	}
	return mcp
}

// AssembleManagedHooks builds the COMPLETE ctxloom-managed hook set that every
// writer of a backend settings file must produce identically: config-level
// hooks, default-profile-shipped hooks, bundle-shipped hooks, and (when
// contextHash is non-empty) the context-injection hook.
//
// Both writers route through this — the `ctxloom run` setup payload
// (AssembleManagedConfig, which passes contextHash "" so the agent appends its
// own injection hook) and operations.ApplyHooks (which passes the resolved
// hash). WriteSettings reconciles by removing ALL ctxloom hooks and re-adding
// only the writer's assembled set, so any divergence between the writers
// silently drops whatever one assembled but the other didn't — the failure
// class that once broke forward-bind. Keeping the full assembly here guarantees
// both writers produce an identical, complete set.
//
// Returns a fresh HooksConfig each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
func AssembleManagedHooks(cfg *config.Config, workDir, contextHash string) *wire.HooksConfig {
	hooks := &wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)}
	if cfg == nil {
		return hooks
	}
	// Config-level hooks.
	agent.MergeHooksConfig(hooks, &cfg.Hooks)
	// Default-profile-shipped hooks.
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName)
		if err != nil {
			continue
		}
		agent.MergeHooksConfig(hooks, &resolved.Hooks)
	}
	// Bundle-shipped hooks + (optional) the context-injection hook.
	appendManagedDynamicHooks(&hooks.Unified, cfg, workDir, contextHash)
	return hooks
}

// appendManagedDynamicHooks appends the ctxloom-managed hooks that are assembled
// dynamically (rather than read verbatim from one config block): the
// bundle-shipped hooks (SCM-tagged — e.g. `session bind`, `stamp-plan`) and,
// when contextHash is non-empty, the SessionStart context-injection hook. The
// `ctxloom run` path passes contextHash "" here and lets the agent append its
// own injection hook from the plugin-side hash; apply-hooks passes the hash.
func appendManagedDynamicHooks(unified *wire.UnifiedHooks, cfg *config.Config, workDir, contextHash string) {
	if unified == nil || cfg == nil {
		return
	}
	unified.Append(cfg.ResolveBundleHooks())
	if contextHash != "" {
		unified.SessionStart = append(unified.SessionStart, agent.NewContextInjectionHooks(contextHash, workDir)...)
	}
}
