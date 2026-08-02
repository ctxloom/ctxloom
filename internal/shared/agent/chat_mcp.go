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
// command exactly as ResolveMCPCommand does for the settings-file writers —
// empty is a no-op, non-empty (populated ONLY for an isolated-container cell)
// wins.
func ComposeChatMCPServers(pluginKey, override string, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, existing []ChatMCPServer) []ChatMCPServer {
	if mcp == nil && bundleMCP == nil {
		return nil
	}

	merged := make(map[string]ChatMCPServer)
	add := func(name, command string, args []string, env map[string]string) {
		merged[name] = ChatMCPServer{Name: name, Command: command, Args: args, Env: env}
	}

	if mcp.ShouldAutoRegisterCtxloom() {
		// This used to hardcode CtxloomCommand() (the host self-exec absolute
		// path) with no override parameter at all, so a runtime:container
		// agent's structured chat baked the HOST's ctxloom binary path into
		// its own mcpServers instead of the in-container path — the same
		// ResolveMCPCommand(override) the settings-file writers (mcpfile.go,
		// codex/claude/etc.) already use.
		add(MCPServerName, ResolveMCPCommand(override), CtxloomMCPArgs, nil)
	}
	for name, s := range bundleMCP {
		add(name, s.Command, s.Args, s.Env)
	}
	addConfiguredServers(add, mcp, pluginKey)

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

// addConfiguredServers folds the last two sources of the managed set into the
// composition, in ComposeChatMCPServers' documented override order: the
// config+profile unified servers, then the engine's OWN passthrough servers
// (mcp.Plugins[pluginKey]), which therefore win a name clash. A nil mcp
// contributes nothing — the caller has already established that some payload
// exists, and "no unified config" is not the same as "no payload".
func addConfiguredServers(add func(name, command string, args []string, env map[string]string), mcp *wire.MCPConfig, pluginKey string) {
	if mcp == nil {
		return
	}
	for name, s := range mcp.Servers {
		add(name, s.Command, s.Args, s.Env)
	}
	for name, s := range mcp.Plugins[pluginKey] {
		add(name, s.Command, s.Args, s.Env)
	}
}

// PatchManagedCommand rewrites the auto-registered ctxloom entry's Command in
// an ALREADY-composed server set to override, leaving every other entry (and
// the set's names/args/env) untouched. It exists for callers that must
// compose the managed set before the isolation policy — and therefore the
// MCP command override — is known (coordinator delegation: coord/spawner.go's
// childMCPServers resolves plan.MCPServers once at Resolve time, before
// Launch/StartEngine ever learn the runtime policy). Re-running
// ComposeChatMCPServers there instead would re-fire the executable trust
// gate's WarnWithheld and violate plan.MCPServers' "resolved exactly once"
// invariant — this patches the one field that can change without touching
// either. override == "" is a no-op (returns servers unchanged); a
// non-empty override without a matching MCPServerName entry is also a no-op
// (nothing to patch — e.g. ShouldAutoRegisterCtxloom was false).
func PatchManagedCommand(servers []ChatMCPServer, override string) []ChatMCPServer {
	if override == "" {
		return servers
	}
	found := false
	for _, s := range servers {
		if s.Name == MCPServerName {
			found = true
			break
		}
	}
	if !found {
		return servers
	}
	out := make([]ChatMCPServer, len(servers))
	copy(out, servers)
	for i := range out {
		if out[i].Name == MCPServerName {
			out[i].Command = override
		}
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
