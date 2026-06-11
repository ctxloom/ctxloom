package grpc

import (
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// This file holds the Go<->proto converters for the host-assembled
// ManagedConfig setup payload. The host serializes its agent.ManagedConfig with
// ManagedConfigToProto onto RunStart; the plugin Run handler deserializes with
// managedConfigFromProto into SetupRequest.Managed. The proto messages mirror
// the shared/wire hook + MCP vocabulary and agent.CommandExport field-for-field,
// so these converters are mechanical — no ctxloom config/bundle type crosses the
// wire, only its resolved, wire-typed result.

// ManagedConfigToProto converts a host-assembled agent.ManagedConfig to its
// proto form. Returns nil for nil input so skip_setup/distill paths send none.
func ManagedConfigToProto(m *agent.ManagedConfig) *ManagedConfig {
	if m == nil {
		return nil
	}
	return &ManagedConfig{
		Prompts:          commandExportsToProto(m.Prompts),
		Hooks:            hooksConfigToProto(m.Hooks),
		Mcp:              mcpConfigToProto(m.MCP),
		BundleMcp:        mcpServerMapToProto(m.BundleMCP),
		ManageStatusline: m.ManageStatusline,
	}
}

// managedConfigFromProto rebuilds an agent.ManagedConfig from its proto form on
// the plugin side. Returns nil for nil input.
func managedConfigFromProto(m *ManagedConfig) *agent.ManagedConfig {
	if m == nil {
		return nil
	}
	return &agent.ManagedConfig{
		Prompts:          commandExportsFromProto(m.GetPrompts()),
		Hooks:            hooksConfigFromProto(m.GetHooks()),
		MCP:              mcpConfigFromProto(m.GetMcp()),
		BundleMCP:        mcpServerMapFromProto(m.GetBundleMcp()),
		ManageStatusline: m.GetManageStatusline(),
	}
}

// --- command exports ---

func commandExportsToProto(in []agent.CommandExport) []*CommandExport {
	if in == nil {
		return nil
	}
	out := make([]*CommandExport, len(in))
	for i, c := range in {
		out[i] = &CommandExport{
			Name:         c.Name,
			Content:      c.Content,
			Enabled:      c.Enabled,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
			AllowedTools: c.AllowedTools,
			Model:        c.Model,
		}
	}
	return out
}

func commandExportsFromProto(in []*CommandExport) []agent.CommandExport {
	if in == nil {
		return nil
	}
	out := make([]agent.CommandExport, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, agent.CommandExport{
			Name:         c.GetName(),
			Content:      c.GetContent(),
			Enabled:      c.GetEnabled(),
			Description:  c.GetDescription(),
			ArgumentHint: c.GetArgumentHint(),
			AllowedTools: c.GetAllowedTools(),
			Model:        c.GetModel(),
		})
	}
	return out
}

// --- hooks ---

func hookToProto(h wire.Hook) *Hook {
	return &Hook{
		Matcher: h.Matcher,
		Command: h.Command,
		Type:    h.Type,
		Prompt:  h.Prompt,
		Timeout: int32(h.Timeout),
		Async:   h.Async,
		Scm:     h.SCM,
	}
}

func hookFromProto(h *Hook) wire.Hook {
	if h == nil {
		return wire.Hook{}
	}
	return wire.Hook{
		Matcher: h.GetMatcher(),
		Command: h.GetCommand(),
		Type:    h.GetType(),
		Prompt:  h.GetPrompt(),
		Timeout: int(h.GetTimeout()),
		Async:   h.GetAsync(),
		SCM:     h.GetScm(),
	}
}

func hooksToProto(hs []wire.Hook) []*Hook {
	if hs == nil {
		return nil
	}
	out := make([]*Hook, len(hs))
	for i, h := range hs {
		out[i] = hookToProto(h)
	}
	return out
}

func hooksFromProto(hs []*Hook) []wire.Hook {
	if len(hs) == 0 {
		return nil
	}
	out := make([]wire.Hook, 0, len(hs))
	for _, h := range hs {
		out = append(out, hookFromProto(h))
	}
	return out
}

func unifiedHooksToProto(u wire.UnifiedHooks) *UnifiedHooks {
	return &UnifiedHooks{
		PreTool:      hooksToProto(u.PreTool),
		PostTool:     hooksToProto(u.PostTool),
		SessionStart: hooksToProto(u.SessionStart),
		SessionEnd:   hooksToProto(u.SessionEnd),
		PreShell:     hooksToProto(u.PreShell),
		PostFileEdit: hooksToProto(u.PostFileEdit),
	}
}

func unifiedHooksFromProto(u *UnifiedHooks) wire.UnifiedHooks {
	if u == nil {
		return wire.UnifiedHooks{}
	}
	return wire.UnifiedHooks{
		PreTool:      hooksFromProto(u.GetPreTool()),
		PostTool:     hooksFromProto(u.GetPostTool()),
		SessionStart: hooksFromProto(u.GetSessionStart()),
		SessionEnd:   hooksFromProto(u.GetSessionEnd()),
		PreShell:     hooksFromProto(u.GetPreShell()),
		PostFileEdit: hooksFromProto(u.GetPostFileEdit()),
	}
}

func hooksConfigToProto(c *wire.HooksConfig) *HooksConfig {
	if c == nil {
		return nil
	}
	out := &HooksConfig{Unified: unifiedHooksToProto(c.Unified)}
	if len(c.Plugins) > 0 {
		out.Plugins = make(map[string]*BackendHooks, len(c.Plugins))
		for name, bh := range c.Plugins {
			events := make(map[string]*HookList, len(bh))
			for event, hooks := range bh {
				events[event] = &HookList{Hooks: hooksToProto(hooks)}
			}
			out.Plugins[name] = &BackendHooks{Events: events}
		}
	}
	return out
}

func hooksConfigFromProto(c *HooksConfig) *wire.HooksConfig {
	if c == nil {
		return nil
	}
	// Match the substrate's assembled shape: a non-nil Plugins map even when empty.
	out := &wire.HooksConfig{
		Unified: unifiedHooksFromProto(c.GetUnified()),
		Plugins: make(map[string]wire.BackendHooks),
	}
	for name, bh := range c.GetPlugins() {
		events := make(wire.BackendHooks)
		for event, list := range bh.GetEvents() {
			events[event] = hooksFromProto(list.GetHooks())
		}
		out.Plugins[name] = events
	}
	return out
}

// --- MCP ---

func mcpServerToProto(s wire.MCPServer) *MCPServer {
	return &MCPServer{
		Command:      s.Command,
		Args:         s.Args,
		Env:          s.Env,
		Notes:        s.Notes,
		Installation: s.Installation,
		Scm:          s.SCM,
	}
}

func mcpServerFromProto(s *MCPServer) wire.MCPServer {
	if s == nil {
		return wire.MCPServer{}
	}
	return wire.MCPServer{
		Command:      s.GetCommand(),
		Args:         s.GetArgs(),
		Env:          s.GetEnv(),
		Notes:        s.GetNotes(),
		Installation: s.GetInstallation(),
		SCM:          s.GetScm(),
	}
}

// mcpServerMapToProto converts a bare name→server map (the bundle MCP set) to
// its proto form. Returns nil for an empty/nil map so the wire stays minimal and
// managedConfigFromProto rebuilds a nil BundleMCP — matching the host's "no
// bundle servers" shape rather than an empty non-nil map.
func mcpServerMapToProto(in map[string]wire.MCPServer) map[string]*MCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*MCPServer, len(in))
	for name, s := range in {
		out[name] = mcpServerToProto(s)
	}
	return out
}

func mcpServerMapFromProto(in map[string]*MCPServer) map[string]wire.MCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]wire.MCPServer, len(in))
	for name, s := range in {
		out[name] = mcpServerFromProto(s)
	}
	return out
}

func mcpConfigToProto(c *wire.MCPConfig) *MCPConfig {
	if c == nil {
		return nil
	}
	out := &MCPConfig{AutoRegisterCtxloom: c.AutoRegisterCtxloom}
	if len(c.Servers) > 0 {
		out.Servers = make(map[string]*MCPServer, len(c.Servers))
		for name, s := range c.Servers {
			out.Servers[name] = mcpServerToProto(s)
		}
	}
	if len(c.Plugins) > 0 {
		out.Plugins = make(map[string]*MCPServerMap, len(c.Plugins))
		for backend, servers := range c.Plugins {
			sm := make(map[string]*MCPServer, len(servers))
			for name, s := range servers {
				sm[name] = mcpServerToProto(s)
			}
			out.Plugins[backend] = &MCPServerMap{Servers: sm}
		}
	}
	return out
}

func mcpConfigFromProto(c *MCPConfig) *wire.MCPConfig {
	if c == nil {
		return nil
	}
	// Match the substrate's assembled shape: non-nil Servers/Plugins maps.
	out := &wire.MCPConfig{
		AutoRegisterCtxloom: c.AutoRegisterCtxloom,
		Servers:             make(map[string]wire.MCPServer),
		Plugins:             make(map[string]map[string]wire.MCPServer),
	}
	for name, s := range c.GetServers() {
		out.Servers[name] = mcpServerFromProto(s)
	}
	for backend, sm := range c.GetPlugins() {
		servers := make(map[string]wire.MCPServer)
		for name, s := range sm.GetServers() {
			servers[name] = mcpServerFromProto(s)
		}
		out.Plugins[backend] = servers
	}
	return out
}
