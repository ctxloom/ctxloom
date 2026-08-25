// Command probe-mcp-server is the ctxloom capability-probe P2 fixture: a stdio
// MCP server serving exactly one tool, whose result is a nonce that exists
// nowhere else an engine can reach.
//
// WHY THIS IS A BINARY AND NOT A SCRIPT: the probe must run on every isolation
// axis, and a container cell runs the server INSIDE the container, as a child of
// the engine. The agent image ships no interpreter beyond a shell, so a scripted
// fixture makes the probe measure interpreter availability instead of the axis
// under test. Go is already this project's toolchain, so the binary costs no new
// dependency.
//
// THE NONCE IS READ FROM A FILE, NEVER FROM ARGV. An MCP server's command and
// args are declared in the project's config.yaml, which is inside the workspace
// and readable by the very agent under test. A nonce on argv would therefore be
// reachable without ever calling the tool, and the probe would pass while
// proving nothing. The nonce file lives beside this binary in a fixture
// directory sited OUTSIDE the workspace.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// toolName is the single tool served. The client may namespace it, so calls are
// matched by suffix as well as exactly.
const toolName = "get_nonce"

// protocolFallback is answered when a client's initialize omits its version.
const protocolFallback = "2025-06-18"

// The fixture directory's three files. These names are the contract between
// this binary and probeMCPWriteFixture, which creates the directory.
const (
	nonceFileName = "nonce.txt"
	logFileName   = "nonce_mcp_calls.jsonl"
)

type server struct {
	nonce   string
	callLog string
	out     *bufio.Writer
}

// record appends one evidence line. O_APPEND plus a single Write keeps records
// whole even if a client starts the server more than once.
//
// Evidence must never take the server down: a crashed server is reported as an
// MCP-DELIVERY failure, which is a different finding from the one this probe is
// trying to make. Every error here is deliberately swallowed.
func (s *server) record(event string, detail map[string]any) {
	line, err := json.Marshal(map[string]any{
		"ts":     float64(time.Now().UnixNano()) / 1e9,
		"pid":    os.Getpid(),
		"event":  event,
		"detail": detail,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.callLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func (s *server) send(msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_, _ = s.out.Write(append(b, '\n'))
	_ = s.out.Flush()
}

func (s *server) reply(id any, result map[string]any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *server) fail(id any, code int, message string) {
	s.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (s *server) handle(req map[string]any) {
	method, _ := req["method"].(string)
	id, hasID := req["id"]
	if id == nil {
		hasID = false
	}
	s.record("request", map[string]any{"method": method, "has_id": hasID})

	// A JSON-RPC NOTIFICATION carries no id and MUST NOT be answered.
	if !hasID {
		return
	}

	params, _ := req["params"].(map[string]any)

	switch method {
	case "initialize":
		version, _ := params["protocolVersion"].(string)
		if version == "" {
			version = protocolFallback
		}
		s.reply(id, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "ctxloom-probe-nonce", "version": "1.0.0"},
		})
	case "tools/list":
		s.reply(id, map[string]any{"tools": []any{map[string]any{
			"name": toolName,
			"description": "Returns this session's nonce string. The value exists nowhere " +
				"else: it cannot be guessed, recalled, or found in context.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}})
	case "tools/call":
		name, isStr := params["name"].(string)
		// A client may present the tool under its own namespaced name.
		if isStr && (name == toolName || strings.HasSuffix(name, toolName)) {
			// THE EVIDENCE LINE. The verdict requires one of these; without it
			// an echoed nonce proves only that something read a file. The
			// returned VALUE is deliberately not recorded — a log carrying the
			// nonce would become a second channel to it.
			s.record("tool_call", map[string]any{"name": name})
			s.reply(id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": s.nonce}},
				"isError": false,
			})
			return
		}
		s.record("tool_call_unknown", map[string]any{"name": params["name"]})
		s.fail(id, -32602, fmt.Sprintf("unknown tool: %#v", params["name"]))
	case "ping":
		s.reply(id, map[string]any{})
	case "resources/list":
		s.reply(id, map[string]any{"resources": []any{}})
	case "prompts/list":
		s.reply(id, map[string]any{"prompts": []any{}})
	default:
		s.fail(id, -32601, fmt.Sprintf("method not found: %#v", method))
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "probe-mcp-server: usage: probe-mcp-server <fixture-dir>\n"+
			"the fixture directory holds "+nonceFileName+" (the nonce this server serves) and "+
			logFileName+" (the evidence log). It is sited outside the workspace on purpose: a "+
			"nonce reachable from the agent's cwd would let the probe pass without a tool call.")
		os.Exit(2)
	}
	dir := os.Args[1]

	// Refusing an empty nonce is the same guard probeMCPWriteFixture makes on
	// the writing side: every string contains the empty string, so a server
	// serving "" would satisfy the round-trip assertion without a round trip.
	raw, err := os.ReadFile(filepath.Join(dir, nonceFileName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe-mcp-server: reading the nonce: %v\n", err)
		os.Exit(2)
	}
	nonce := strings.TrimSpace(string(raw))
	if nonce == "" {
		fmt.Fprintln(os.Stderr, "probe-mcp-server: refusing to serve an empty nonce — "+
			"every string contains the empty string, so the round-trip assertion would pass "+
			"without a round trip")
		os.Exit(2)
	}

	s := &server{
		nonce:   nonce,
		callLog: filepath.Join(dir, logFileName),
		out:     bufio.NewWriter(os.Stdout),
	}

	s.record("start", map[string]any{"argv": os.Args[1:]})

	sc := bufio.NewScanner(os.Stdin)
	// An MCP frame is one JSON object per line, and a tools/list reply carrying
	// a long description can exceed bufio's default 64KiB ceiling. A scanner
	// that stops mid-session would be reported as a delivery failure.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.record("unparseable", map[string]any{"bytes": len(line)})
			continue
		}
		s.handle(req)
	}
	s.record("eof", map[string]any{})
}
