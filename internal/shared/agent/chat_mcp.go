package agent

import (
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// ComposeChatMCPServers maps the ctxloom-managed MCP server set onto
// caller-supplied chat servers (ChatRequest.MCPServers → session/new
// mcpServers) for the structured paths, which never run backend Setup and so
// never get its settings-file write. The source is the one
// MCPFileConfig.WriteServers reconciles into an engine's settings file:
// bundle-shipped servers (config.ResolveBundleMCPServers — the builtin
// bundles, each discovered companion's own loadout, and the profile→bundle
// cascade), so the two delivery paths cannot diverge. A name already present
// in existing is dropped: the caller's explicit entry wins, so a
// client-supplied session server is never duplicated. The result is
// name-sorted for a deterministic frame.
//
// nil bundleMCP means no managed payload was assembled (config load failed, or
// setup was skipped): nothing is injected, mirroring BaseLifecycle.MergeManaged's
// no-op on a nil ManagedConfig.
//
// override is the in-container ctxloom path for an isolated-container cell,
// applied to ctxloom's OWN entry by ResolveManagedMCPServers — empty is a
// no-op.
func ComposeChatMCPServers(override string, bundleMCP map[string]wire.MCPServer, existing []ChatMCPServer) []ChatMCPServer {
	if bundleMCP == nil {
		return nil
	}

	merged := make(map[string]ChatMCPServer)
	for name, s := range ResolveManagedMCPServers(bundleMCP, override) {
		merged[name] = ChatMCPServer{Name: name, Command: s.Command, Args: s.Args, Env: s.Env}
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

// PatchManagedCommand rewrites the ctxloom entry's Command in an ALREADY-composed
// server set to override, leaving every other entry (and the set's names/args/env)
// untouched. It exists for callers that must compose the managed set before the
// isolation policy — and therefore the MCP command override — is known
// (coordinator delegation: coord/spawner.go's childMCPServers resolves
// plan.MCPServers once at Resolve time, before Launch/StartEngine ever learn the
// runtime policy). Re-running ComposeChatMCPServers there instead would re-fire
// the executable trust gate's WarnWithheld and violate plan.MCPServers' "resolved
// exactly once" invariant — this patches the one field that can change without
// touching either. override == "" is a no-op (returns servers unchanged); a
// non-empty override without a matching MCPServerName entry is also a no-op
// (nothing to patch — the builtin ctxloom bundle's server was withheld).
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
func (m *ManagedConfig) ChatMCPServers(override string) []ChatMCPServer {
	if m == nil {
		return nil
	}
	return ComposeChatMCPServers(override, m.BundleMCP, nil)
}
