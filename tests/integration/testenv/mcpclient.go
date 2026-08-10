package testenv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// sessionEnvKeys are the ambient session / forge variables scrubbed before the
// MCP server is spawned, so a mock-agent test never inherits the host session's
// project id, harp, or tokens and resolves its home-rooted stores against our
// fake home instead. Sourced from the canonical testsupport key list so there is
// one definition of "ambient state to isolate from".
var sessionEnvKeys = func() map[string]bool {
	m := make(map[string]bool, len(testsupport.EnvKeys))
	for _, k := range testsupport.EnvKeys {
		m[k] = true
	}
	return m
}()

// mcpRecvTimeout bounds how long a single response is waited for. Handlers
// respond in milliseconds; the window is generous so the suite stays reliable on
// slow CI without hanging a broken server indefinitely.
const mcpRecvTimeout = 15 * time.Second

// MCPClient drives `ctxloom mcp` as an MCP client over stdio (JSON-RPC 2.0). It
// is the single shared door for mock-agent test traffic: integration and
// acceptance suites speak to the server through this type rather than
// re-implementing the wire plumbing. Every method returns an error (no
// *testing.T) so it composes with both Go tests and godog steps.
// MCPClient is single-goroutine: every request method reads and increments
// nextID unsynchronized. Nothing here is safe for concurrent use
// from more than one goroutine -- unlike Command (environment.go), whose doc
// explicitly advertises building many concurrently, MCPClient carries no such
// contract and must not be treated as if it did.
type MCPClient struct {
	cmd     *exec.Cmd
	ctx     context.Context // cancelled by Close; unblocks a parked reader send
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	lines   chan string // server stdout, one line per receive; closed on read error
	readErr error       // why lines closed; written before close, read only after
	nextID  int         // request id counter; single-goroutine only, see type doc

	// initResult is the raw "initialize" response envelope, captured by
	// Initialize so callers can inspect what the server advertised (e.g.
	// Instructions) without re-implementing the handshake. Nil until
	// Initialize succeeds.
	initResult map[string]any
}

// StartMCP spawns the MCP server in the project directory with the isolated
// (and session-scrubbed — see isolatedEnv) environment. extraEnv entries
// ("KEY=VALUE") are appended last and win over the isolated environment.
//
// The argv is agent.CtxloomMCPArgs, the SAME value ctxloom writes into every
// engine's own MCP settings, so this harness drives the exact invocation an
// engine drives. Spelling it out here instead would let the two drift, and a
// harness that speaks the protocol to a spelling no engine uses proves
// nothing about what engines get.
func (e *TestEnvironment) StartMCP(extraEnv ...string) (*MCPClient, error) {
	return startMCPProcess(e.AppBinary, agent.CtxloomMCPArgs, e.ProjectDir, e.isolatedEnv(), extraEnv...)
}

// startMCPProcess spawns bin(args...) as an MCP server over stdio in dir, with
// baseEnv plus extraEnv ("KEY=VALUE", appended last so it wins). Factored out
// of StartMCP so a second binary (taskloom, see taskloom_acceptance.go) can
// reuse the exact same process/reader plumbing rather than re-implementing it.
func startMCPProcess(bin string, args []string, dir string, baseEnv []string, extraEnv ...string) (*MCPClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv, extraEnv...)
	// See pdeathsig_linux.go: this MCP server can itself delegate to an
	// agent (agent_run) that spawns an "llm serve <label>" plugin
	// subprocess (the same self-invoking path `ctxloom run` uses), so it
	// gets the same "die with the test binary" guarantee `ctxloom run`
	// invocations do.
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcp stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // surface ctxloom warnings to the test log

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start mcp server: %w", err)
	}
	c := &MCPClient{
		cmd:    cmd,
		ctx:    ctx,
		cancel: cancel,
		stdin:  stdin,
		lines:  make(chan string, 64),
		nextID: 1,
	}
	// Dedicated reader goroutine so recvByID can select on a timer: a
	// wedged-but-alive server (stdout open, nothing arriving) then times out
	// instead of hanging the suite inside a blocking ReadString. The goroutine
	// exits when the server's stdout closes (server death or Close's kill).
	go func() {
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				// Select on ctx.Done so a burst of unconsumed lines that fills
				// c.lines (cap 64) can't park this goroutine forever: Close's
				// cancel() then unblocks the send and lets it exit cleanly.
				select {
				case c.lines <- line:
				case <-c.ctx.Done():
					c.readErr = c.ctx.Err()
					close(c.lines)
					return
				}
			}
			if err != nil {
				c.readErr = err
				close(c.lines)
				return
			}
		}
	}()
	return c, nil
}

// scrubSessionEnv returns env with the ambient session variables removed,
// without mutating the input slice.
func scrubSessionEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if sessionEnvKeys[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Close terminates the server subprocess. cancel (exec.CommandContext's
// default Cancel, a raw Process.Kill) can fire before the server's own
// graceful stdin-EOF shutdown completes, hard-killing it — so any "llm serve
// <label>" plugin subprocess it spawned via agent delegation (setsid'd into
// its own session, unreachable by anything this harness could signal as a
// group) is captured by pid BEFORE that kill and explicitly reaped
// afterward, the same defense-in-depth as ptyrun.go's PTYSession.Close and
// for the identical reason: a killed parent never runs its own cleanup.
func (c *MCPClient) Close() error {
	if c == nil {
		return nil
	}
	var pid int
	if c.cmd.Process != nil {
		pid = c.cmd.Process.Pid
	}
	children := PluginChildrenOf(pid)

	_ = c.stdin.Close()
	if c.cancel != nil {
		c.cancel()
	}
	_ = c.cmd.Wait()

	KillPids(children)
	return nil
}

// send writes one newline-delimited JSON-RPC request to the server's stdin.
// Its former near-duplicate, internal/agentbus/socket.go's writeObserveEvent
// (the retired bus protocol's line writer), was deleted whole in D2 — this
// is now the sole surviving instance of the marshal-then-newline-write
// shape, kept here rather than shared: it serves a different wire (stdio
// JSON-RPC, not the Unix-socket bus protocol) and a different layer (test
// harness, not production).
func (c *MCPClient) send(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// recvByID consumes lines until a response with the given id arrives, skipping
// notifications and non-JSON noise. The deadline is enforced even while no
// line is arriving (reader goroutine + timer select), so a wedged-but-alive
// server times the call out instead of hanging the suite.
func (c *MCPClient) recvByID(id int) (map[string]any, error) {
	timer := time.NewTimer(mcpRecvTimeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return nil, fmt.Errorf("read stdout (id=%d): %w", id, c.readErr)
			}
			var msg map[string]any
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if rid, ok := msg["id"].(float64); ok && int(rid) == id {
				return msg, nil
			}
		case <-timer.C:
			return nil, fmt.Errorf("timeout waiting for response id=%d", id)
		}
	}
}

// Initialize performs the initialize / notifications/initialized handshake.
func (c *MCPClient) Initialize() error {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "ctxloom-acceptance", "version": "0"},
		},
	}); err != nil {
		return err
	}
	msg, err := c.recvByID(id)
	if err != nil {
		return err
	}
	c.initResult = msg
	return c.send(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
}

// Instructions returns the server's initialize-response "instructions"
// string (the top-level Instructions a client sees before ever calling a
// tool), or "" if the server advertised none or Initialize has not
// succeeded yet.
func (c *MCPClient) Instructions() string {
	result, _ := c.initResult["result"].(map[string]any)
	s, _ := result["instructions"].(string)
	return s
}

// ToolResult is a tools/call response envelope with helpers to unwrap the inner
// JSON the server embeds in content[0].text.
type ToolResult struct {
	Raw map[string]any
}

// Inner unwraps the embedded operation-result JSON. It returns an error when the
// call produced a JSON-RPC error or the envelope is malformed.
func (r ToolResult) Inner() (map[string]any, error) {
	if e := r.Raw["error"]; e != nil {
		return nil, fmt.Errorf("tool error: %v", e)
	}
	result, ok := r.Raw["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("result is not a map: %v", r.Raw)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return nil, fmt.Errorf("result.content missing or empty")
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("content[0] is not a map")
	}
	text, ok := first["text"].(string)
	if !ok {
		return nil, fmt.Errorf("content[0].text is not a string")
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(text), &inner); err != nil {
		return nil, fmt.Errorf("unwrap tool json: %w", err)
	}
	return inner, nil
}

// IsError reports whether the tool call failed and a short failure message.
// Failure means EITHER a JSON-RPC envelope error OR a CallToolResult with
// isError=true — the form the MCP SDK uses for handler errors and input
// validation failures, which never populate the JSON-RPC envelope error. The
// "succeeds"/"fails" assertions must consult this, not just Raw["error"].
func (r ToolResult) IsError() (bool, string) {
	if e := r.Raw["error"]; e != nil {
		return true, fmt.Sprintf("%v", e)
	}
	result, ok := r.Raw["result"].(map[string]any)
	if !ok {
		return false, ""
	}
	if isErr, _ := result["isError"].(bool); isErr {
		return true, toolContentText(result)
	}
	return false, ""
}

// toolContentText returns content[0].text from a CallToolResult, or "" if absent.
func toolContentText(result map[string]any) string {
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return ""
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := first["text"].(string)
	return text
}

// JSON returns the raw inner-result text for substring assertions, or the raw
// envelope when no inner text is present.
func (r ToolResult) JSON() string {
	data, _ := json.Marshal(r.Raw)
	return string(data)
}

// CallTool fires tools/call and returns the response envelope.
func (c *MCPClient) CallTool(name string, args map[string]any) (ToolResult, error) {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}); err != nil {
		return ToolResult{}, err
	}
	msg, err := c.recvByID(id)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Raw: msg}, nil
}

// listNames fires a list method and extracts a string field from each element
// of a named result array (e.g. method "tools/list", array "tools", field
// "name").
func (c *MCPClient) listNames(method, array, field string) ([]string, error) {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	msg, err := c.recvByID(id)
	if err != nil {
		return nil, err
	}
	if e := msg["error"]; e != nil {
		return nil, fmt.Errorf("%s error: %v", method, e)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: result is not a map", method)
	}
	return parseNamedArray(result, array, field)
}

// parseNamedArray extracts a string field from each element of a named array
// in result, returning an ERROR (not a nil slice) when the array key is
// absent or not an array — a malformed response must not be
// indistinguishable from a well-formed empty list: a server
// advertising zero tools/resources looked identical to one whose response
// never parsed, and the acceptance suite's only completeness gate passed
// vacuously against either.
func parseNamedArray(result map[string]any, array, field string) ([]string, error) {
	raw, present := result[array]
	if !present {
		return nil, fmt.Errorf("result has no %q key", array)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("result[%q] is not an array (got %T)", array, raw)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m[field].(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// ListTools returns the names of every registered MCP tool.
func (c *MCPClient) ListTools() ([]string, error) {
	return c.listNames("tools/list", "tools", "name")
}

// ToolDetail is one tool's name, static description, and raw input schema
// (marshaled back to JSON text) from a tools/list response — additive
// alongside ListTools (names only) for surfaces that need to assert what a
// tool's description or schema actually document, not just that the tool
// exists.
type ToolDetail struct {
	Name        string
	Description string
	SchemaJSON  string
}

// ListToolDetails fires tools/list and returns each tool's name, description,
// and input schema (as raw JSON text, for substring assertions against
// per-field jsonschema descriptions the schema — not the static
// Description — carries).
func (c *MCPClient) ListToolDetails() ([]ToolDetail, error) {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/list", "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	msg, err := c.recvByID(id)
	if err != nil {
		return nil, err
	}
	if e := msg["error"]; e != nil {
		return nil, fmt.Errorf("tools/list error: %v", e)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tools/list: result is not a map")
	}
	rawTools, present := result["tools"]
	if !present {
		return nil, fmt.Errorf("tools/list: result has no \"tools\" key")
	}
	items, ok := rawTools.([]any)
	if !ok {
		return nil, fmt.Errorf("tools/list: result[\"tools\"] is not an array (got %T)", rawTools)
	}
	out := make([]ToolDetail, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		desc, _ := m["description"].(string)
		schemaJSON := ""
		if schema, ok := m["inputSchema"]; ok {
			if b, err := json.Marshal(schema); err == nil {
				schemaJSON = string(b)
			}
		}
		out = append(out, ToolDetail{Name: name, Description: desc, SchemaJSON: schemaJSON})
	}
	return out, nil
}

// ListResources returns the URIs of every fixed MCP resource.
func (c *MCPClient) ListResources() ([]string, error) {
	return c.listNames("resources/list", "resources", "uri")
}

// ListResourceTemplates returns the URI templates of every templated resource.
func (c *MCPClient) ListResourceTemplates() ([]string, error) {
	return c.listNames("resources/templates/list", "resourceTemplates", "uriTemplate")
}

// ReadResource fires resources/read and returns the first content's text and
// MIME type.
func (c *MCPClient) ReadResource(uri string) (text, mime string, err error) {
	id := c.nextID
	c.nextID++
	if err = c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "resources/read",
		"params": map[string]any{"uri": uri},
	}); err != nil {
		return "", "", err
	}
	msg, err := c.recvByID(id)
	if err != nil {
		return "", "", err
	}
	if e := msg["error"]; e != nil {
		return "", "", fmt.Errorf("resource error: %v", e)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("result is not a map")
	}
	contents, ok := result["contents"].([]any)
	if !ok || len(contents) == 0 {
		return "", "", fmt.Errorf("result.contents missing or empty")
	}
	first, _ := contents[0].(map[string]any)
	text, _ = first["text"].(string)
	mime, _ = first["mimeType"].(string)
	return text, mime, nil
}
