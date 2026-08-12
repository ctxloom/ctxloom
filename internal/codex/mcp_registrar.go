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

// MCPRegistrar implements agent.MCPRegistrar for Codex: `.codex/config.toml`
// under the project for project scope, under the home dir for user scope.
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

// ConfigPath returns the MCP config file for the scope. Global scope resolves
// Codex's home via codexHome ($CODEX_HOME, else ~/.codex) so the path matches
// where codex actually reads its global config — the same precedence used by
// codexPromptsDir and getSessionsDir. Project scope resolves through
// ProjectHome — the harpless S7-interim project-root join CodexHookWriter.
// SettingsPath uses (which folds [mcp_servers] into the very same file), so
// this registrar and that writer can never name two different config.tomls for
// one project.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		home, err := codexHome()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ConfigFileName), nil
	}
	return filepath.Join(ProjectHome(dir), ConfigFileName), nil
}

// Install merges the named server into the config bytes. Idempotent; foreign
// tables and keys are preserved.
func (MCPRegistrar) Install(config []byte, name string, server wire.MCPServer) ([]byte, error) {
	doc, err := mcpTOMLDoc(config)
	if err != nil {
		return nil, err
	}
	servers, ok := doc["mcp_servers"].(map[string]any)
	if !ok {
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
