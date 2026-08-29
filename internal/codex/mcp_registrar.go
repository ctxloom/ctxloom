//go:build parked_engines

package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPRegistrar implements agent.MCPRegistrar for Codex: `config.toml` under
// $CODEX_HOME for user scope, and NO project scope at all (see ConfigPath).
// Servers live in the `[mcp_servers.<name>]` table.
//
// The merge round-trips through a TOML document model, so unknown tables and
// keys survive; TOML comments do not (the file is machine-managed in
// practice — ctxloom regenerates it wholesale).
type MCPRegistrar struct{}

var _ agent.MCPRegistrar = MCPRegistrar{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return "codex" }

// Present reports whether Codex appears to be in use for the scope.
func (r MCPRegistrar) Present(dir string, global bool) bool {
	p, err := r.ConfigPath(dir, global)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Dir(p))
	return err == nil
}

// ConfigPath returns the MCP config file for the scope.
//
// GLOBAL scope resolves Codex's home via codexHome ($CODEX_HOME, else
// ~/.codex) so the path matches where codex actually reads its global config —
// the same precedence used by codexPromptsDir and getSessionsDir. This is the
// scope that still works, and it is the scope that matters: codex's servers
// have only ever been home-keyed.
//
// PROJECT scope REFUSES. There is no project-scoped config.toml — codex folds
// [mcp_servers] into the very same file CodexHookWriter has no project-keyed
// path for (declared_absence.go), so this registrar and that writer agree by
// both declining rather than by naming one dead path twice. An error, not "",
// because every caller of this joins onto the result: a caller handed "" writes
// a stray config.toml at the filesystem root.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		home, err := codexHome()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ConfigFileName), nil
	}
	return "", launchOnlyError("codex has no project-scoped MCP configuration file")
}

// Install merges the named server into the config bytes. Idempotent; foreign
// tables and keys are preserved.
//
// A PRESENT "mcp_servers" of the wrong TOML type (a string, an array, ...) is
// REFUSED rather than silently replaced with a fresh empty table — matching
// its JSON twin, agent.InstallMCPServerJSON (M5 asymmetry, R6/config-patching-
// review.md bypass B6). Before this, only an ABSENT key took the fresh-table
// path; "present but wrong type" fell through the same `!ok` branch and got
// clobbered, destroying whatever the user (or a hand-edited config.toml) had
// there. Only "absent" earns the fresh table now.
func (MCPRegistrar) Install(config []byte, name string, server wire.MCPServer) ([]byte, error) {
	doc, err := mcpTOMLDoc(config)
	if err != nil {
		return nil, err
	}
	servers, ok := doc["mcp_servers"].(map[string]any)
	if !ok {
		if existing, present := doc["mcp_servers"]; present {
			return nil, fmt.Errorf("mcp_servers is %T, not a table — refusing to overwrite it", existing)
		}
		servers = map[string]any{}
		doc["mcp_servers"] = servers
	}
	servers[name] = mcpServerToTOMLEntry(server)
	return toml.Marshal(doc)
}

// Uninstall removes the named server from the config bytes.
//
// A server that is not there is a BYTE-FOR-BYTE no-op, not a round-trip: this
// document model drops every comment, so re-marshalling a file nothing was
// removed from silently rewrites it — and a config.toml holding only comments
// re-marshals to ZERO BYTES, which the caller writes over the user's file
// while reporting a successful removal.
//
// Emptying the table deletes the key, so the two writers of this file agree
// about emptiness: removeManagedMCP (settings.go) drops an emptied
// [mcp_servers] rather than leaving the bare stanza behind.
func (MCPRegistrar) Uninstall(config []byte, name string) ([]byte, error) {
	doc, err := mcpTOMLDoc(config)
	if err != nil {
		return nil, err
	}
	servers, ok := doc["mcp_servers"].(map[string]any)
	if !ok {
		return config, nil
	}
	if _, present := servers[name]; !present {
		return config, nil
	}
	delete(servers, name)
	if len(servers) == 0 {
		delete(doc, "mcp_servers")
	}
	return toml.Marshal(doc)
}

// Installed reports whether the named server is present in the config.
func (MCPRegistrar) Installed(config []byte, name string) (bool, error) {
	doc, err := mcpTOMLDoc(config)
	if err != nil {
		return false, err
	}
	servers, ok := doc["mcp_servers"].(map[string]any)
	if !ok {
		return false, nil
	}
	_, present := servers[name]
	return present, nil
}

func mcpTOMLDoc(config []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(config)) == 0 {
		return doc, nil
	}
	if err := toml.Unmarshal(config, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return doc, nil
}
