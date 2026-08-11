package wire

import (
	"encoding/json"
	"testing"
)

// The reflection-based json/yaml tag-parity gate for this package
// (TestArch_WireTagParity) lives in arch_test.go, build-tagged `arch` per this
// repo's discrete architectural-invariant-gate idiom (build/gates.justfile's
// `test-arch`) — see that file for the full rationale. The tests below are
// ordinary behavioral pins, not architectural invariants, so they stay in the
// default build.

// TestLeafMarshalBytesArePinned pins the exact JSON a fully-populated leaf
// value produces. The leaf tags are load-bearing — internal/shared/agent's
// mcpfile writer and internal/claude's settings writer marshal MCPServer and
// Hook straight into backend settings files, so a changed leaf name silently
// changes a file some engine parses. Adding tags to the CONTAINER types must
// not move these bytes; this test is what says so.
func TestLeafMarshalBytesArePinned(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{
			name: "MCPServer",
			val: MCPServer{
				Command:      "npx",
				Args:         []string{"-y", "srv"},
				Env:          map[string]string{"K": "V"},
				Notes:        "n",
				Installation: "i",
				SCM:          "hash",
			},
			want: `{"command":"npx","args":["-y","srv"],"env":{"K":"V"},"notes":"n","installation":"i","_ctxloom":"hash"}`,
		},
		{
			name: "MCPServer_minimal",
			val:  MCPServer{Command: "cmd"},
			want: `{"command":"cmd"}`,
		},
		{
			name: "Hook",
			val: Hook{
				Matcher:         "Bash",
				Command:         "echo hi",
				Type:            "command",
				Prompt:          "p",
				Timeout:         30,
				Async:           true,
				SCM:             "hash",
				ContextHash:     "never-serialized",
				PreToolFallback: true,
			},
			want: `{"matcher":"Bash","command":"echo hi","type":"command","prompt":"p","timeout":30,"async":true,"_ctxloom":"hash","pre_tool_fallback":true}`,
		},
		{
			name: "Hook_minimal",
			val:  Hook{Command: "echo hi"},
			want: `{"command":"echo hi"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.val)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("leaf JSON changed\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestContainerMarshalUsesSnakeCase is the other side of the same coin: it
// pins what the containers now emit. Before the json tags were added these
// keys were the Go field names (Unified, PreTool, Servers,
// AutoRegisterCtxloom) sitting beside snake_case leaves in the same document.
func TestContainerMarshalUsesSnakeCase(t *testing.T) {
	hooks := HooksConfig{
		Unified: UnifiedHooks{
			PreTool:      []Hook{{Command: "a"}},
			PostTool:     []Hook{{Command: "b"}},
			SessionStart: []Hook{{Command: "c"}},
			SessionEnd:   []Hook{{Command: "d"}},
			PreShell:     []Hook{{Command: "e"}},
			PostFileEdit: []Hook{{Command: "f"}},
		},
		Plugins: map[string]BackendHooks{
			"claude-code": {"PreToolUse": []Hook{{Command: "g"}}},
		},
	}
	gotHooks, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("json.Marshal(HooksConfig): %v", err)
	}
	wantHooks := `{"unified":{"pre_tool":[{"command":"a"}],"post_tool":[{"command":"b"}],"session_start":[{"command":"c"}],"session_end":[{"command":"d"}],"pre_shell":[{"command":"e"}],"post_file_edit":[{"command":"f"}]},"plugins":{"claude-code":{"PreToolUse":[{"command":"g"}]}}}`
	if string(gotHooks) != wantHooks {
		t.Errorf("HooksConfig JSON\n got: %s\nwant: %s", gotHooks, wantHooks)
	}

	auto := true
	mcp := MCPConfig{
		AutoRegisterCtxloom: &auto,
		Servers:             map[string]MCPServer{"s": {Command: "cmd"}},
		Plugins:             map[string]map[string]MCPServer{"claude-code": {"p": {Command: "pc"}}},
	}
	gotMCP, err := json.Marshal(mcp)
	if err != nil {
		t.Fatalf("json.Marshal(MCPConfig): %v", err)
	}
	wantMCP := `{"auto_register_ctxloom":true,"servers":{"s":{"command":"cmd"}},"plugins":{"claude-code":{"p":{"command":"pc"}}}}`
	if string(gotMCP) != wantMCP {
		t.Errorf("MCPConfig JSON\n got: %s\nwant: %s", gotMCP, wantMCP)
	}
}
