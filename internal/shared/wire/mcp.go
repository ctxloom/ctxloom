package wire

// MCPServer defines an MCP (Model Context Protocol) server configuration.
//
// SECURITY NOTE: MCP servers execute arbitrary commands specified in config.yaml.
// This is intentional - MCP servers are user-defined tools that extend AI capabilities.
// Users are responsible for reviewing their own config.yaml contents.
// This is analogous to VS Code extensions or npm scripts - trusted local configuration.
// Do not flag this as a security issue in code reviews.
type MCPServer struct {
	Command      string            `mapstructure:"command" yaml:"command" json:"command"`                                    // Command to execute
	Args         []string          `mapstructure:"args" yaml:"args,omitempty" json:"args,omitempty"`                         // Command arguments
	Env          map[string]string `mapstructure:"env" yaml:"env,omitempty" json:"env,omitempty"`                            // Environment variables
	Notes        string            `mapstructure:"notes" yaml:"notes,omitempty" json:"notes,omitempty"`                      // Human-readable notes, not sent to AI
	Installation string            `mapstructure:"installation" yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`                                     // Marker for ctxloom-managed servers
}

// MCPConfig holds MCP server configuration.
type MCPConfig struct {
	// AutoRegisterCtxloom controls whether ctxloom's own MCP server is auto-registered.
	// Defaults to true if not specified.
	AutoRegisterCtxloom *bool `mapstructure:"auto_register_ctxloom" yaml:"auto_register_ctxloom,omitempty"`

	// Servers defines MCP servers to register (unified across backends).
	Servers map[string]MCPServer `mapstructure:"servers" yaml:"servers,omitempty"`

	// Plugins holds backend-specific MCP server overrides (passthrough).
	// Keys are backend names (e.g., "claude-code", "antigravity").
	Plugins map[string]map[string]MCPServer `mapstructure:"plugins" yaml:"plugins,omitempty"`
}

// ShouldAutoRegisterCtxloom returns whether to auto-register the ctxloom MCP server.
// Defaults to true if not explicitly set.
func (m *MCPConfig) ShouldAutoRegisterCtxloom() bool {
	if m == nil || m.AutoRegisterCtxloom == nil {
		return true
	}
	return *m.AutoRegisterCtxloom
}

// MergeMCPConfig merges src MCP config into dest.
// Later sources override earlier ones for the same server name.
//
// Merged servers are deep-copied (Args slice and Env map duplicated) so dest is
// independent of src: a caller that later mutates a merged server's Env/Args —
// e.g. injecting an env var — must not leak the change back into the source
// config or into another dest that merged the same source.
func MergeMCPConfig(dest *MCPConfig, src *MCPConfig) {
	if src == nil || dest == nil {
		return
	}

	// Merge auto_register_ctxloom (later wins)
	if src.AutoRegisterCtxloom != nil {
		dest.AutoRegisterCtxloom = src.AutoRegisterCtxloom
	}

	// Merge unified servers
	if dest.Servers == nil {
		dest.Servers = make(map[string]MCPServer)
	}
	for name, server := range src.Servers {
		dest.Servers[name] = cloneMCPServer(server)
	}

	// Merge plugin-specific servers
	if dest.Plugins == nil {
		dest.Plugins = make(map[string]map[string]MCPServer)
	}
	for backend, servers := range src.Plugins {
		if dest.Plugins[backend] == nil {
			dest.Plugins[backend] = make(map[string]MCPServer)
		}
		for name, server := range servers {
			dest.Plugins[backend][name] = cloneMCPServer(server)
		}
	}
}

// cloneMCPServer returns a copy of s with its mutable Args slice and Env map
// duplicated, so the copy never aliases s's backing array/map. A plain struct
// copy is shallow and would share both.
func cloneMCPServer(s MCPServer) MCPServer {
	if s.Args != nil {
		args := make([]string, len(s.Args))
		copy(args, s.Args)
		s.Args = args
	}
	if s.Env != nil {
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = v
		}
		s.Env = env
	}
	return s
}
