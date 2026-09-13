//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestAssertToolCallSucceeds_FailsWhenResultCannotBeUnwrapped pins that
// callTool used to discard Inner()'s error entirely
// (w.lastInner, _ = res.Inner()), leaving lastInner nil with no signal —
// a malformed or error result was indistinguishable from a well-formed
// one simply missing the field being asserted, and "the tool call
// succeeds" (which only checked IsError) could pass on a payload that
// never parsed at all.
func TestAssertToolCallSucceeds_FailsWhenResultCannotBeUnwrapped(t *testing.T) {
	// A well-formed, non-error result whose content is simply absent —
	// IsError() reports false (no isError flag, no JSON-RPC error), but
	// Inner() cannot unwrap it.
	tool := toolOutcome{res: &mcp.CallToolResult{}}
	_, innerErr := tool.Inner()
	if innerErr == nil {
		t.Fatal("test fixture invalid: expected tool.Inner() to fail on a contentless result")
	}

	w := &World{lastTool: tool, lastInnerErr: innerErr}
	err := assertToolCallSucceeds(w)
	if err == nil {
		t.Fatal("expected assertToolCallSucceeds to fail when the result could not be unwrapped, got nil")
	}
}

// TestAssertToolCallSucceeds_PassesOnAWellFormedResult is the ordinary
// success path.
func TestAssertToolCallSucceeds_PassesOnAWellFormedResult(t *testing.T) {
	tool := toolOutcome{res: &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: `{"ok":true}`}},
	}}
	inner, innerErr := tool.Inner()
	if innerErr != nil {
		t.Fatalf("test fixture invalid: %v", innerErr)
	}

	w := &World{lastTool: tool, lastInner: inner}
	if err := assertToolCallSucceeds(w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestToolOutcome_IsError_ReportsBothFailureShapes pins the two ways a tool
// call fails — a JSON-RPC error answer and an isError result — as both
// counting, each carrying the server's own message.
func TestToolOutcome_IsError_ReportsBothFailureShapes(t *testing.T) {
	rpc := toolOutcome{rpcErr: &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown tool"}}
	if isErr, msg := rpc.IsError(); !isErr || msg == "" {
		t.Fatalf("JSON-RPC error outcome: IsError() = %v, %q; want true with a message", isErr, msg)
	}
	handler := toolOutcome{res: &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "refused: reserved for the coordinator"}},
	}}
	if isErr, msg := handler.IsError(); !isErr || msg != "refused: reserved for the coordinator" {
		t.Fatalf("isError result: IsError() = %v, %q; want true with the content text", isErr, msg)
	}
	if isErr, _ := (toolOutcome{}).IsError(); isErr {
		t.Fatal("zero outcome must not read as an error")
	}
}
