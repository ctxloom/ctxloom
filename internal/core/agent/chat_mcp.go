package agent

import (
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ComposeChatMCPServers maps the ctxloom-managed MCP server set onto
// caller-supplied chat servers (ChatRequest.MCPServers → session/new
// mcpServers) for the structured paths, which never run backend Setup and so
// never get its settings-file write. The source is the one
// claude.ClaudeCodeHookWriter.writeMCPConfig reconciles into an engine's MCP
// registry file: bundle-shipped servers (config.ResolveBundleMCPServers — the builtin
// bundles, each discovered companion's own loadout, and the profile→bundle
// cascade), so the two delivery paths cannot diverge. A name already present
// in existing is dropped: the caller's explicit entry wins, so a
// client-supplied session server is never duplicated. The result is
// name-sorted for a deterministic frame.
//
// nil bundleMCP means no managed payload was assembled (config load failed, or
// setup was skipped): nothing is injected, mirroring BaseLifecycle.MergeManaged's
// no-op on a nil ManagedConfig.
func ComposeChatMCPServers(bundleMCP map[string]wire.MCPServer, existing []ChatMCPServer) []ChatMCPServer {
	if bundleMCP == nil {
		return nil
	}

	merged := make(map[string]ChatMCPServer)
	for name, s := range bundleMCP {
		merged[name] = ChatMCPServerFromWire(name, s)
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

// ChatMCPServerFromWire is the one conversion from ctxloom's engine-neutral
// server (wire.MCPServer) to the chat/engine-file shape, and therefore the one
// place the transport discriminator is DERIVED. wire.MCPServer deliberately
// stores no transport — the URL is the transport — so a URL entry becomes an
// http-transport server (Streamable HTTP; every scheme Validate admits is
// that protocol) carrying URL and Headers, and anything else is a stdio
// command. It does not validate: callers that can fail loud
// (InstallMCPServerJSON, the settings writers) run wire.MCPServer.Validate
// first, so a targetless entry is refused by name rather than dialled as
// nothing.
func ChatMCPServerFromWire(name string, s wire.MCPServer) ChatMCPServer {
	if s.IsRemote() {
		return ChatMCPServer{Name: name, Transport: MCPTransportHTTP, URL: s.URL, Headers: s.Headers}
	}
	return ChatMCPServer{Name: name, Command: s.Command, Args: s.Args, Env: s.Env}
}

// ChatMCPServers composes the chat-injectable server set from a host-assembled
// managed payload — the SAME payload RunStart ships to Setup — for a
// structured run that bypasses Setup. A nil payload injects nothing (the host
// assembled none; Setup would have flushed nothing either).
func (m *ManagedConfig) ChatMCPServers() []ChatMCPServer {
	if m == nil {
		return nil
	}
	return ComposeChatMCPServers(m.BundleMCP, nil)
}
