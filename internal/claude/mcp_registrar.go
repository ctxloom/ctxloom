package claude

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/confpatch"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPRegistrar is taskloom's registrar for Claude Code: project-scope servers
// live in `.mcp.json` at the project root (where variable expansion works),
// user-scope servers in `~/.claude.json` — both in the JSON "mcpServers" table
// shape. It is the engine.Engine implementation taskloom's registry holds.
type MCPRegistrar struct{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return EngineName }

// Present reports whether Claude Code appears to be in use for the scope.
func (MCPRegistrar) Present(dir string, global bool) bool {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		return pathExists(filepath.Join(home, ".claude.json")) || pathExists(filepath.Join(home, ".claude"))
	}
	return pathExists(filepath.Join(dir, ".mcp.json")) || pathExists(filepath.Join(dir, ".claude"))
}

// ConfigPath returns the MCP config file for the scope.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude.json"), nil
	}
	return filepath.Join(dir, ".mcp.json"), nil
}

// Register writes the named server into the config at path through store, by
// the same byte-preserving patch ClaudeCodeHookWriter uses for ctxloom's own
// servers; a nil server is the uninstall. The name is always an owned path:
// with no record to reverse, an entry already there is taken out only if it
// runs the store owner's executable — a user's own server parked under the
// same name is left exactly where it is.
func (MCPRegistrar) Register(fs afero.Fs, store *confpatch.Store, path, name string, server *wire.MCPServer, opts ...confpatch.ApplyOption) (confpatch.Result, error) {
	desired := map[string]any{}
	if server != nil {
		entry, err := agent.MCPServerJSONEntry(name, *server)
		if err != nil {
			return confpatch.Result{}, err
		}
		desired[name] = entry
	}
	return applyMCPServers(fs, store, path, desired, []string{name}, opts...)
}

// Installed reports whether the named server is present in the config.
func (MCPRegistrar) Installed(config []byte, name string) (bool, error) {
	return agent.MCPServerInstalledJSON(config, name)
}

// pathExists reports whether the path exists (file or directory).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
