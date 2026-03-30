package backends

import (
	"github.com/SophisticatedContextManager/scm/internal/config"
)

// BaseMCPManager provides shared MCP server management logic for backends.
type BaseMCPManager struct {
	backendName string
	servers     map[string]MCPServer
}

// NewBaseMCPManager creates a new MCP manager for the given backend.
func NewBaseMCPManager(backendName string) *BaseMCPManager {
	return &BaseMCPManager{
		backendName: backendName,
		servers:     make(map[string]MCPServer),
	}
}

// RegisterServer adds an MCP server to the backend configuration.
func (m *BaseMCPManager) RegisterServer(workDir string, server MCPServer) error {
	m.ensureServers()
	m.servers[server.Name] = server
	return nil
}

// UnregisterServer removes an MCP server from the backend configuration.
func (m *BaseMCPManager) UnregisterServer(workDir string, name string) error {
	m.ensureServers()
	delete(m.servers, name)
	return nil
}

// ListServers returns the names of registered MCP servers.
func (m *BaseMCPManager) ListServers(workDir string) ([]string, error) {
	m.ensureServers()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	return names, nil
}

// GetServer returns the configuration for a specific MCP server.
func (m *BaseMCPManager) GetServer(workDir string, name string) (*MCPServer, error) {
	m.ensureServers()
	if srv, ok := m.servers[name]; ok {
		return &srv, nil
	}
	return nil, nil
}

// Clear removes all SCM-managed MCP servers.
func (m *BaseMCPManager) Clear(workDir string) error {
	m.servers = make(map[string]MCPServer)
	return m.Flush(workDir)
}

// Flush writes all pending MCP configuration changes.
func (m *BaseMCPManager) Flush(workDir string) error {
	m.ensureServers()

	// Convert to config.MCPConfig
	mcpCfg := &config.MCPConfig{
		Servers: make(map[string]config.MCPServer),
	}
	for name, srv := range m.servers {
		mcpCfg.Servers[name] = config.MCPServer{
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
		}
	}

	// Write settings (hooks are nil, just MCP)
	return WriteSettings(m.backendName, nil, mcpCfg, nil, workDir)
}

func (m *BaseMCPManager) ensureServers() {
	if m.servers == nil {
		m.servers = make(map[string]MCPServer)
	}
}
