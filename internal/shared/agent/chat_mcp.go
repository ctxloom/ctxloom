package agent

import (
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// ComposeChatMCPServers maps the ctxloom-managed MCP server set onto
// caller-supplied chat servers (ChatRequest.MCPServers → session/new
// mcpServers) for the structured paths, which never run backend Setup and so
// never get its settings-file write. The sources and their override order are
// exactly the ones MCPFileConfig.WriteServers reconciles into an engine's
// settings file — the auto-registered ctxloom server (unless disabled),
// bundle-shipped servers (ResolveBundleMCPServers: the profile→bundle
// cascade plus each discovered companion's own loadout — see
// internal/config/companions.go), the config+profile unified servers, then
// the engine's own passthrough servers
// (pluginKey) — so the two delivery paths cannot diverge. A name already
// present in existing is dropped: the caller's explicit entry wins, so a
// client-supplied session server is never duplicated. The result is
// name-sorted for a deterministic frame.
//
// nil mcp AND nil bundleMCP means no managed payload was assembled (config
// load failed, or setup was skipped): nothing is injected, mirroring
// BaseLifecycle.MergeManaged's no-op on a nil ManagedConfig.
//
// override replaces CtxloomCommand() as the auto-registered ctxloom server's
// command exactly as ResolveMCPCommand does for the settings-file writers
// (U100-F03) — empty is a no-op, non-empty (populated ONLY for an
// isolated-container cell) wins.
func ComposeChatMCPServers(pluginKey, override string, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, existing []ChatMCPServer) []ChatMCPServer {
	if mcp == nil && bundleMCP == nil {
		return nil
	}

	merged := make(map[string]ChatMCPServer)
	add := func(name, command string, args []string, env map[string]string) {
		merged[name] = ChatMCPServer{Name: name, Command: command, Args: args, Env: env}
	}

	if mcp.ShouldAutoRegisterCtxloom() {
		// U100-F03: this hardcoded CtxloomCommand() (the host self-exec
		// absolute path) with no override parameter at all, so a
		// runtime:container agent's structured chat baked the HOST's ctxloom
		// binary path into its own mcpServers instead of the in-container
		// path — the same ResolveMCPCommand(override) the settings-file
		// writers (mcpfile.go, codex/claude/etc.) already use.
		add(MCPServerName, ResolveMCPCommand(override), CtxloomMCPArgs, nil)
	}
	for name, s := range bundleMCP {
		add(name, s.Command, s.Args, s.Env)
	}
	if mcp != nil {
		for name, s := range mcp.Servers {
			add(name, s.Command, s.Args, s.Env)
		}
		for name, s := range mcp.Plugins[pluginKey] {
			add(name, s.Command, s.Args, s.Env)
		}
	}

	for _, e := range existing {
		delete(merged, e.Name)
	}
	if len(merged) == 0 {
		return nil
	}
	out := make([]ChatMCPServer, 0, len(merged))
	for _, name := range slices.Sorted(maps.Keys(merged)) {
		out = append(out, merged[name])
	}
	return out
}

// ChatMCPServers composes the chat-injectable server set from a host-assembled
// managed payload — the SAME payload RunStart ships to Setup — for a
// structured run that bypasses Setup. A nil payload injects nothing (the host
// assembled none; Setup would have flushed nothing either).
func (m *ManagedConfig) ChatMCPServers(pluginKey, override string) []ChatMCPServer {
	if m == nil {
		return nil
	}
	return ComposeChatMCPServers(pluginKey, override, m.MCP, m.BundleMCP, nil)
}
