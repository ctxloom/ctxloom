//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestAssertToolCallSucceeds_FailsWhenEnvelopeCannotBeUnwrapped pins
// U162-F04: callTool used to discard Inner()'s error entirely
// (w.lastInner, _ = res.Inner()), leaving lastInner nil with no signal —
// a malformed or error envelope was indistinguishable from a well-formed
// one simply missing the field being asserted, and "the tool call
// succeeds" (which only checked IsError) could pass on a payload that
// never parsed at all.
func TestAssertToolCallSucceeds_FailsWhenEnvelopeCannotBeUnwrapped(t *testing.T) {
	// A well-formed, non-error JSON-RPC envelope whose result.content is
	// simply absent — IsError() reports false (no isError flag, no
	// top-level "error"), but Inner() cannot unwrap it.
	tool := testenv.ToolResult{Raw: map[string]any{
		"result": map[string]any{},
	}}
	_, innerErr := tool.Inner()
	if innerErr == nil {
		t.Fatal("test fixture invalid: expected tool.Inner() to fail on a contentless result")
	}

	w := &World{lastTool: tool, lastInnerErr: innerErr}
	err := assertToolCallSucceeds(w)
	if err == nil {
		t.Fatal("expected assertToolCallSucceeds to fail when the envelope could not be unwrapped, got nil")
	}
}

// TestAssertToolCallSucceeds_PassesOnAWellFormedEnvelope is the ordinary
// success path, unchanged by the fix.
func TestAssertToolCallSucceeds_PassesOnAWellFormedEnvelope(t *testing.T) {
	tool := testenv.ToolResult{Raw: map[string]any{
		"result": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": `{"ok":true}`},
			},
		},
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
