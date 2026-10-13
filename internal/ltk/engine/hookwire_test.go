package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE HOOK WIRE (ruling #28) carries one tool claude has none of: apply_patch,
// which opencode's bridge passes under its own name and input. ltk reads the
// one file a patch touches; a patch it cannot read whole (several files, or
// none) is ungated, which evaluate denies rather than checking half of it.
func TestClaudeCodeDecodeApplyPatch(t *testing.T) {
	for _, c := range []struct {
		name, payload string
		want          Request
	}{
		{"one-file patch", `{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"patchText":"*** Begin Patch\n*** Update File: /p/a.go\n@@\n-a\n+b\n*** End Patch"}}`,
			Request{ToolName: "apply_patch", FilePath: "/p/a.go"}},
		{"two-file patch", `{"tool_name":"apply_patch","tool_input":{"patchText":"*** Add File: a\n+x\n*** Delete File: b\n"}}`,
			Request{ToolName: "apply_patch", ToolUngated: true}},
		{"empty patch", `{"tool_name":"apply_patch","tool_input":{"patchText":""}}`,
			Request{ToolName: "apply_patch", ToolUngated: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ClaudeCode{}.Decode([]byte(c.payload))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// apply_patch is read off the wire but never installed into claude's own
// settings: claude has no such tool.
func TestClaudeMatcherLeavesOutWireOnlyTools(t *testing.T) {
	assert.NotContains(t, strings.Split(claudeMatcher, "|"), "apply_patch")
}
