package claude

import (
	"encoding/json"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// This file is claude's stream-json protocol for the structured driver
// (kit.ProcessTurn, assembled in instance.go): the user message written to
// the process's stdin as NDJSON, and the chat-only flags. The NDJSON event
// stream from stdout is normalized by turnStream (chat_stream.go).

// ErrChatMCPTransportUnsupported is returned when an MCP server entry names a
// transport claude's --mcp-config file cannot express. Remedy: only
// MCPTransportStdio, MCPTransportHTTP, and MCPTransportSSE are valid. Aliases
// agent.ErrChatMCPConfigTransportUnsupported (the shared marshal helper's own
// sentinel) under this package's existing name, so callers checking
// errors.Is(err, ErrChatMCPTransportUnsupported) are unaffected by the move.
var ErrChatMCPTransportUnsupported = agent.ErrChatMCPConfigTransportUnsupported

// sjUserOut is the NDJSON user message written to stdin, matching claude's
// stream-json input schema: {"type":"user","message":{"role":"user","content":...}}.
type sjUserOut struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

func writeUserMessage(w io.Writer, text string) error {
	var m sjUserOut
	m.Type = "user"
	m.Message.Role = "user"
	m.Message.Content = text
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// flagInputFormat, flagVerbose, flagResume: chat-only argv flags that have no
// existing constant in enginecli.go (the oneshot/interactive EngineCLI
// declarations never emit them — this structured path is a third surface).
// flagPrint, flagOutputFormat, flagModel, flagMCPConfig are reused from
// enginecli.go rather than redeclared here, and permission-mode flags are
// reused from permissionArgs (claudecode.go) rather than re-mapped.
const (
	flagInputFormat = "--input-format"
	flagVerbose     = "--verbose"
	flagResume      = "--resume"
)
