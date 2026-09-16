package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Byte-level MCP registration helpers for the JSON "mcpServers" table shape
// that Claude Code (.mcp.json / ~/.claude.json) and Kiro
// (.kiro/settings/mcp.json) share. Unlike the SettingsWriter path — which
// reconciles the ctxloom-managed server set against a config file — these
// operate on raw config bytes and register a single named server, preserving
// every foreign key. They are the seam external registrars (taskloom manage)
// use so per-agent config formats never leak out of the agent modules.

// InstallMCPServerJSON merges the named server into a JSON config document
// under "mcpServers", preserving every other key. A nil or empty config
// yields a fresh document. Idempotent.
func InstallMCPServerJSON(config []byte, name string, server wire.MCPServer) ([]byte, error) {
	doc, err := mcpJSONDoc(config)
	if err != nil {
		return nil, err
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		// An absent key is a legitimate fresh install, but a
		// PRESENT value of the wrong type (a string, array, etc.) used to
		// take the same branch and get silently REPLACED with a fresh empty
		// map, destroying whatever the user had there. Only "absent" earns
		// the fresh map; "present but wrong type" is reported instead.
		if existing, present := doc["mcpServers"]; present {
			return nil, fmt.Errorf("mcpServers is %T, not an object — refusing to overwrite it", existing)
		}
		servers = map[string]any{}
		doc["mcpServers"] = servers
	}
	entry, err := mcpJSONEntry(name, server)
	if err != nil {
		return nil, err
	}
	servers[name] = entry
	return mcpJSONRender(doc)
}

// mcpJSONEntry renders server as the generic map an "mcpServers" table holds,
// through the shared entry shape so stdio and remote spell identically to
// every other writer. The JSON round trip is what honours the entry's tags
// (omitempty in particular) when the caller merges into an untyped document.
func mcpJSONEntry(name string, server wire.MCPServer) (map[string]any, error) {
	if err := server.Validate(); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	entry, err := ChatMCPConfigEntryOf(ChatMCPServerFromWire(name, server))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode mcp server %q: %w", name, err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("encode mcp server %q: %w", name, err)
	}
	return generic, nil
}

// UninstallMCPServerJSON removes the named server, preserving everything
// else. Removing an absent server is a no-op, not an error.
func UninstallMCPServerJSON(config []byte, name string) ([]byte, error) {
	doc, err := mcpJSONDoc(config)
	if err != nil {
		return nil, err
	}
	if servers, ok := doc["mcpServers"].(map[string]any); ok {
		delete(servers, name)
	}
	return mcpJSONRender(doc)
}

// MCPServerInstalledJSON reports whether the named server exists in the
// document.
func MCPServerInstalledJSON(config []byte, name string) (bool, error) {
	doc, err := mcpJSONDoc(config)
	if err != nil {
		return false, err
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		return false, nil
	}
	_, present := servers[name]
	return present, nil
}

func mcpJSONDoc(config []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(config)) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	// UseNumber keeps foreign numeric values exact: a float64 round-trip
	// would lose precision on integers beyond 2^53 and can re-render large
	// ints in exponent form, breaking the preserve-foreign-keys contract.
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return doc, nil
}

func mcpJSONRender(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
