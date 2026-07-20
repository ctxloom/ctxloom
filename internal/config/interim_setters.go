package config

import (
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// INTERIM (Phase 3 of the config-manager rework): the six known production
// write sites (operations/agents.go SetAgent/RemoveAgent,
// operations/mcp_servers.go's add/remove/SetMCPAutoRegister,
// operations/manage.go's SetStatusline, operations/tooling.go's
// ScaffoldContainerBase, cli/agent.go's default-agent set) mutate a *Config
// directly and then call Save(). Phase 3 unexports every field, which breaks
// those six call sites with nowhere to redirect to; Phase 4 replaces all of
// them with Manager.Update(func(*Draft) error), which supplies the mutex, the
// filelock spanning the FULL read-modify-write span, and the atomic save —
// this file exists ONLY to keep Phase 3 compiling and green in between, and
// is deleted once Phase 4 lands. These narrow setters mutate the SAME shared
// *Config the six sites already mutated today (the pre-existing bug this
// whole rework fixes is not made worse OR better by this file — it is
// unchanged from Phase 2's behavior); they add no new capability, they only
// let the existing capability keep compiling with unexported fields.

// SetAgentEntry sets or replaces one entry in the `agents:` config-key map,
// creating the map if absent.
func (c *Config) SetAgentEntry(name string, a agents.Agent) {
	if c.agents == nil {
		c.agents = make(map[string]agents.Agent)
	}
	c.agents[name] = a
}

// HasAgentEntry reports whether name exists in the `agents:` config-key map
// (not the merged directory view — see LoadAgents/Agent for that).
func (c *Config) HasAgentEntry(name string) bool {
	_, ok := c.agents[name]
	return ok
}

// DeleteAgentEntry removes name from the `agents:` config-key map.
func (c *Config) DeleteAgentEntry(name string) {
	delete(c.agents, name)
}

// SetMCPServer sets or replaces one entry in the unified MCP server map,
// creating the map if absent.
func (c *Config) SetMCPServer(name string, s wire.MCPServer) {
	if c.mcp.Servers == nil {
		c.mcp.Servers = make(map[string]wire.MCPServer)
	}
	c.mcp.Servers[name] = s
}

// HasMCPServer reports whether name exists in the unified MCP server map.
func (c *Config) HasMCPServer(name string) bool {
	_, ok := c.mcp.Servers[name]
	return ok
}

// DeleteMCPServer removes name from the unified MCP server map, reporting
// whether it was present.
func (c *Config) DeleteMCPServer(name string) bool {
	if _, ok := c.mcp.Servers[name]; !ok {
		return false
	}
	delete(c.mcp.Servers, name)
	return true
}

// SetMCPPluginServer sets or replaces one entry in a backend's MCP plugin
// map, creating both maps if absent.
func (c *Config) SetMCPPluginServer(backend, name string, s wire.MCPServer) {
	if c.mcp.Plugins == nil {
		c.mcp.Plugins = make(map[string]map[string]wire.MCPServer)
	}
	if c.mcp.Plugins[backend] == nil {
		c.mcp.Plugins[backend] = make(map[string]wire.MCPServer)
	}
	c.mcp.Plugins[backend][name] = s
}

// HasMCPPluginServer reports whether name exists under backend in the MCP
// plugin map.
func (c *Config) HasMCPPluginServer(backend, name string) bool {
	_, ok := c.mcp.Plugins[backend][name]
	return ok
}

// DeleteMCPPluginServer removes name from backend's MCP plugin map, reporting
// whether it was present.
func (c *Config) DeleteMCPPluginServer(backend, name string) bool {
	servers, ok := c.mcp.Plugins[backend]
	if !ok {
		return false
	}
	if _, ok := servers[name]; !ok {
		return false
	}
	delete(servers, name)
	return true
}

// MCPPluginBackendNames returns every backend name with at least one plugin
// MCP server entry.
func (c *Config) MCPPluginBackendNames() []string {
	out := make([]string, 0, len(c.mcp.Plugins))
	for backend := range c.mcp.Plugins {
		out = append(out, backend)
	}
	return out
}

// SetMCPAutoRegisterCtxloom sets the `mcp.auto_register_ctxloom` flag.
func (c *Config) SetMCPAutoRegisterCtxloom(enabled bool) {
	c.mcp.AutoRegisterCtxloom = &enabled
}

// SetStatuslineEnabled sets the `config.statusline` flag.
func (c *Config) SetStatuslineEnabled(enabled bool) {
	c.settings.Statusline = &enabled
}

// SetIsolationBaseContainerfile sets the `isolation_base_containerfile` path.
func (c *Config) SetIsolationBaseContainerfile(path string) {
	c.isolationBaseContainerfile = path
}

// SetDefaultAgent sets the `default_agent` key.
func (c *Config) SetDefaultAgent(name string) {
	c.defaultAgent = name
}
