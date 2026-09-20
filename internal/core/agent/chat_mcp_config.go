package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ChatMCPConfigDoc is the on-disk shape a caller's --mcp-config-style file
// takes: {"mcpServers": {name: entry}}. This is the wire format claude's
// --mcp-config flag reads today; any future engine writer whose own MCP
// registry file takes the same table shape can reuse MarshalChatMCPConfig /
// WriteChatMCPConfigFile rather than re-deriving it (see chat_mcp_config_test.go
// for the exact byte shape each transport produces).
type ChatMCPConfigDoc struct {
	MCPServers map[string]ChatMCPConfigEntry `json:"mcpServers"`
}

// ChatMCPConfigEntry is one server's entry in a ChatMCPConfigDoc: a local
// stdio command (Type left empty, Command/Args/Env meaningful) or a remote
// http/sse server (Type set from the transport, URL/Headers meaningful,
// Command/Args/Env absent). MCPTransport's own string values ("", "http",
// "sse") are this wire vocabulary's own `type` values, so they pass straight
// through.
//
// Cwd is claude's per-server working directory; only the settings writer sets
// it (on ctxloom's own entry), a chat scratch file never does.
type ChatMCPConfigEntry struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ChatMCPConfigEntryOf renders one server as its table entry: a stdio
// command or a typed remote endpoint. It is the ONE place the entry shape is
// spelled, shared by the chat scratch file, the settings writers and the
// byte-level registrar, so a remote server cannot come out as {"command":""}
// in one of them and correctly in another.
func ChatMCPConfigEntryOf(s ChatMCPServer) (ChatMCPConfigEntry, error) {
	switch s.Transport {
	case MCPTransportStdio:
		return ChatMCPConfigEntry{Command: s.Command, Args: s.Args, Env: s.Env}, nil
	case MCPTransportHTTP, MCPTransportSSE:
		return ChatMCPConfigEntry{Type: string(s.Transport), URL: s.URL, Headers: s.Headers}, nil
	default:
		return ChatMCPConfigEntry{}, fmt.Errorf("%w %q for server %q (supported: stdio, http, sse)",
			ErrChatMCPConfigTransportUnsupported, s.Transport, s.Name)
	}
}

// ErrChatMCPConfigTransportUnsupported is returned by MarshalChatMCPConfig
// when a ChatMCPServer names a transport this JSON table shape cannot
// express. Today only MCPTransportStdio, MCPTransportHTTP, and
// MCPTransportSSE are valid.
var ErrChatMCPConfigTransportUnsupported = errors.New("mcp config: unsupported MCP transport")

// MarshalChatMCPConfig renders servers into the {"mcpServers": {...}} JSON
// document bytes a --mcp-config-style file expects. No I/O — callers that
// need the document without writing a file (a test, an in-memory diff) get
// it directly; WriteChatMCPConfigFile below is the write half.
//
// Each server's own Env map is preserved VERBATIM: a server's Env is the
// composed configuration's, and a table that dropped it would hand the
// engine a server that starts and cannot reach what it was configured for.
func MarshalChatMCPConfig(servers []ChatMCPServer) ([]byte, error) {
	doc := ChatMCPConfigDoc{MCPServers: make(map[string]ChatMCPConfigEntry, len(servers))}
	for _, s := range servers {
		entry, err := ChatMCPConfigEntryOf(s)
		if err != nil {
			return nil, err
		}
		doc.MCPServers[s.Name] = entry
	}
	return json.Marshal(doc)
}

// WriteChatMCPConfigFile marshals servers (MarshalChatMCPConfig) and writes
// them to path at mode 0o600 via iox.WriteFileAtomic — unique temp + fsync +
// exact-perm chmod + rename, rather than a raw file write, because this file
// can carry MCP server auth headers/env and the 0o600 mode must land
// exactly, not masked by umask.
func WriteChatMCPConfigFile(path string, servers []ChatMCPServer) error {
	data, err := MarshalChatMCPConfig(servers)
	if err != nil {
		return err
	}
	return iox.WriteFileAtomic(path, data, 0o600)
}
