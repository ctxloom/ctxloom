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
type MCPClient struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

// StartMCP spawns the MCP server in the project directory with the isolated
// environment, additionally scrubbing ambient session vars. extraEnv entries
// ("KEY=VALUE") are appended last and win over the isolated environment.
func (e *TestEnvironment) StartMCP(extraEnv ...string) (*MCPClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, e.AppBinary, "mcp")
	cmd.Dir = e.ProjectDir
	cmd.Env = append(scrubSessionEnv(e.isolatedEnv()), extraEnv...)

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
	return &MCPClient{
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		nextID: 1,
	}, nil
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

// Close terminates the server subprocess.
func (c *MCPClient) Close() error {
	if c == nil {
		return nil
	}
	_ = c.stdin.Close()
	if c.cancel != nil {
		c.cancel()
	}
	_ = c.cmd.Wait()
	return nil
}

func (c *MCPClient) send(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// recvByID consumes lines until a response with the given id arrives, skipping
// notifications and non-JSON noise.
func (c *MCPClient) recvByID(id int) (map[string]any, error) {
	deadline := time.Now().Add(mcpRecvTimeout)
	for time.Now().Before(deadline) {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read stdout (id=%d): %w", id, err)
		}
		var msg map[string]any
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if rid, ok := msg["id"].(float64); ok && int(rid) == id {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("timeout waiting for response id=%d", id)
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
	if _, err := c.recvByID(id); err != nil {
		return err
	}
	return c.send(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
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
	items, ok := result[array].([]any)
	if !ok {
		return nil, nil
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
