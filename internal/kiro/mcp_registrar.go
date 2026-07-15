package kiro

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPRegistrar implements agent.MCPRegistrar for Kiro CLI: project-scope
// servers live in `.kiro/settings/mcp.json` under the project (the same file
// KiroWriter.mcpFile reconciles via the ledger-tracked path), user-scope
// servers in `$KIRO_HOME/settings/mcp.json` (default `~/.kiro/settings/
// mcp.json` — KIRO_HOME is the same override the session-history reader and
// the worktree isolation policy use to relocate kiro's global state). Both
// scopes use the JSON "mcpServers" table shape — identical to Claude Code's
// and Antigravity's, so this reuses the same generic byte-level helpers
// rather than the ledger-based agent.MCPFileConfig reconciler: this
// registrar gives an external tool (taskloom manage) single-server,
// ledger-free registration, mirroring the other three engines' registrars.
type MCPRegistrar struct{}

var _ agent.MCPRegistrar = MCPRegistrar{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return "kiro" }

// Present reports whether Kiro CLI appears to be in use for the scope: its
// well-known config directory exists.
func (MCPRegistrar) Present(dir string, global bool) bool {
	if global {
		home, err := kiroHome()
		if err != nil {
			return false
		}
		return pathExistsKiro(home)
	}
	return pathExistsKiro(filepath.Join(dir, kiroDir))
}

// ConfigPath returns the MCP config file for the scope.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		home, err := kiroHome()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "settings", "mcp.json"), nil
	}
	return filepath.Join(dir, kiroDir, "settings", "mcp.json"), nil
}

// Install merges the named server into the config bytes. Idempotent; foreign
// keys and servers (including remote url entries) are preserved.
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

// kiroHome resolves Kiro's global config home: $KIRO_HOME if set, else
// ~/.kiro. Mirrors kiroSessionHistory.storeDir's resolution (session.go) —
// KIRO_HOME/~/.kiro holds agents/settings/skills/steering as siblings of
// sessions/ — and codex's codexHome() pattern (commandfiles.go).
func kiroHome() (string, error) {
	if home := os.Getenv("KIRO_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, kiroDir), nil
}

// pathExistsKiro reports whether the path exists (file or directory).
func pathExistsKiro(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
