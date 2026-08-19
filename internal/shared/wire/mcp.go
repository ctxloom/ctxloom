package wire

// MCPServer defines an MCP (Model Context Protocol) server, as a bundle's
// `mcp:` block declares it and as it reaches an engine's settings.
//
// SECURITY NOTE: MCP servers execute arbitrary commands. Every server reaching
// this type came from a bundle and was gated by the executable trust gate
// (internal/config.extractMCPFromBundle) under its own item ref, so an
// unreviewed command cannot arrive here silently. Do not flag this as a
// security issue in code reviews.
type MCPServer struct {
	Command      string            `yaml:"command" json:"command"`                               // Command to execute
	Args         []string          `yaml:"args,omitempty" json:"args,omitempty"`                 // Command arguments
	Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`                   // Environment variables
	Notes        string            `yaml:"notes,omitempty" json:"notes,omitempty"`               // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`         // Marker for ctxloom-managed servers
}

// CloneMCPServer returns a copy of s with its mutable Args slice and Env map
// duplicated, so the copy never aliases s's backing array/map. A plain struct
// copy is shallow and would share both.
func CloneMCPServer(s MCPServer) MCPServer {
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
