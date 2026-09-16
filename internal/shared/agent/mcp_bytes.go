package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Read-side helpers for the JSON "mcpServers" table shape Claude Code
// (.mcp.json / ~/.claude.json) uses, plus the one entry renderer every writer
// of that table shares. They are the seam an external registrar (taskloom
// manage) uses so the per-agent config format never leaks out of the agent
// module. The WRITE side is confpatch: a registrar patches the named member in
// place and records what it wrote, rather than re-encoding the document.

// MCPServerJSONEntry renders server as the generic map an "mcpServers" table
// holds, through the shared entry shape so stdio and remote spell identically
// to every other writer. The JSON round trip is what honours the entry's tags
// (omitempty in particular) when the caller merges into an untyped document:
// hew encodes whatever Go value it is handed, and handing it the struct
// directly would spell the keys by their Go field names.
func MCPServerJSONEntry(name string, server wire.MCPServer) (map[string]any, error) {
	if err := server.Validate(); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	entry, err := ChatMCPConfigEntryOf(ChatMCPServerFromWire(name, server))
	if err != nil {
		return nil, err
	}
	return GenericMCPEntry(name, entry)
}

// GenericMCPEntry is the JSON round trip of one rendered entry to the generic
// value a byte-preserving patch is handed.
func GenericMCPEntry(name string, entry ChatMCPConfigEntry) (map[string]any, error) {
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
