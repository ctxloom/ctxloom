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
// mcp.json` — KIRO_HOME is the same override the worktree isolation policy
// relocates per agent, which moves kiro's config and session state but NOT its
// credentials: those live in a global sqlite outside every kiro home var, which
// is why kiro's isolation spec sets HonoursVarForCreds false — see
// internal/lm/isolation/auth.go). Both
// scopes use the JSON "mcpServers" table shape — identical to Claude Code's,
// so this reuses the same generic byte-level helpers
// rather than the ledger-based agent.MCPFileConfig reconciler: this
// registrar gives an external tool (taskloom manage) single-server,
// ledger-free registration, mirroring the other engines' registrars.
type MCPRegistrar struct{}

var _ agent.MCPRegistrar = MCPRegistrar{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return "kiro" }

// Present reports whether Kiro CLI appears to be in use for the scope: its
// well-known config directory exists.
//
// The contract is a plain bool, so its single "no" carries three different
// meanings: kiro is genuinely absent, the global home could not be resolved, and
// the path could not be stat'ed. Only the first is a fact about kiro; the other
// two are "unknown" and would otherwise make a caller's auto-detection skip kiro
// exactly as if it were unused. They cannot be returned, so they are warned.
func (MCPRegistrar) Present(dir string, global bool) bool {
	if global {
		home, err := kiroHome()
		if err != nil {
			agent.Warn("kiro: cannot resolve kiro's global config home, so its presence is unknown (treating it as absent; select it explicitly to override): %v", err)
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
// ~/.kiro — KIRO_HOME/~/.kiro holds agents/settings/skills/steering as
// siblings of the (formerly scraped, tough-cloud S5-deleted) sessions/ dir —
// and codex's codexHome() pattern (commandfiles.go).
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

// pathExistsKiro reports whether the path exists (file or directory). An absent
// path is the legitimate "not in use here" answer; any other stat failure means
// the answer is unknown, which the bool cannot express — so it is warned (see
// Present).
func pathExistsKiro(path string) bool {
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		agent.Warn("kiro: cannot determine whether %s exists, so kiro's presence is unknown (treating it as absent; select it explicitly to override): %v", path, err)
	}
	return err == nil
}

// GlobalHome returns kiro's global config home with NO workDir context —
// kiroHome()'s own precedence ($KIRO_HOME, else ~/.kiro) — exported for
// the kiro descriptor's hookGlobalScopePaths (flaky-spool: the kiro
// analog of prim-guy's claude $HOME-collision guard and comfy-lion's codex
// one — kiro was audited and found vulnerable to the same collision class).
func GlobalHome() (string, error) { return kiroHome() }

// ProjectHome returns the project-scoped .kiro dir the static
// apply/materialize path targets for workDir (KiroWriter.agentPath /
// mcpPath / steeringPath all join off this same directory), exported for the
// same external collision check as GlobalHome.
func ProjectHome(workDir string) string { return filepath.Join(workDir, kiroDir) }
