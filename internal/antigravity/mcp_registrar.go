package antigravity

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// ErrNoGlobalMCPConfig: agy was observed to read MCP servers only from the
// workspace .agents/mcp_config.json — no global location consulted (probed
// empirically on v1.0.7: ~/.gemini/config/mcp_config.json and
// ~/.gemini/antigravity-cli/mcp_config.json were both ignored). agy's own 1.1.5
// documentation DOES list ~/.gemini/config/mcp_config.json as the global
// location and does not mention the workspace file, so this project-only scope
// rests on the empirical result alone and is due a re-probe.
var ErrNoGlobalMCPConfig = errors.New("antigravity has no user-level MCP config; use project scope")

// MCPRegistrar implements agent.MCPRegistrar for Antigravity CLI (agy):
// `.agents/mcp_config.json` under the project, JSON "mcpServers" table shape
// (same as Claude Code's). Project scope only — see ErrNoGlobalMCPConfig.
type MCPRegistrar struct{}

var _ agent.MCPRegistrar = MCPRegistrar{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return "antigravity" }

// Present reports whether Antigravity appears to be in use for the scope.
func (MCPRegistrar) Present(dir string, global bool) bool {
	if global {
		return false // no global config to register into
	}
	if _, err := os.Stat(filepath.Join(dir, AgentsDir)); err != nil {
		return false
	}
	return true
}

// ConfigPath returns the MCP config file for the scope.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		return "", ErrNoGlobalMCPConfig
	}
	return filepath.Join(dir, AgentsDir, "mcp_config.json"), nil
}

// Install merges the named server into the config bytes. Idempotent; foreign
// servers (including remote serverUrl entries) are preserved.
func (MCPRegistrar) Install(config []byte, name string, server wire.MCPServer) ([]byte, error) {
	return agent.InstallMCPServerJSON(config, name, server)
}

// Uninstall removes the named server from the config bytes.
func (MCPRegistrar) Uninstall(config []byte, name string) ([]byte, error) {
	return agent.UninstallMCPServerJSON(config, name)
}

// Installed reports whether the named server is present in the config.
func (MCPRegistrar) Installed(config []byte, name string) (bool, error) {
	return agent.MCPServerInstalledJSON(config, name)
}
