package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// permissionRequest is a PermissionRequest payload for tool and input with
// claude's own suggestions given whole.
func permissionRequest(tool, input, suggestions string) []byte {
	return []byte(`{"hook_event_name":"PermissionRequest","tool_name":"` + tool + `","tool_input":` + input + `,"permission_suggestions":` + suggestions + `}`)
}

// TestApprovalCodec_SuggestsTheExactCallAndTheWholeTool: after claude's own
// allow rules come the rule for exactly this call — where claude's syntax
// can spell one that matches nothing else — and the rule for the whole
// tool. A rule claude already suggested is not offered twice.
func TestApprovalCodec_SuggestsTheExactCallAndTheWholeTool(t *testing.T) {
	for name, tc := range map[string]struct {
		tool, input, suggestions string
		want                     []string
	}{
		"bash": {"Bash", `{"command":"touch a1","description":"d"}`, `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch *"}],"behavior":"allow","destination":"localSettings"}]`,
			[]string{"Bash(touch *)", "Bash(touch a1)", "Bash"}},
		"a command with a wildcard has no exact rule: claude would read the * as one": {"Bash", `{"command":"rm *.tmp"}`, `[]`,
			[]string{"Bash"}},
		"a multi-line command has no exact rule": {"Bash", `{"command":"echo a\necho b"}`, `[]`,
			[]string{"Bash"}},
		"a blank command has no exact rule": {"Bash", `{"command":"  "}`, `[]`,
			[]string{"Bash"}},
		"claude's own exact rule is not repeated": {"Bash", `{"command":"ls"}`, `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"ls"},{"toolName":"Bash"}],"behavior":"allow","destination":"session"}]`,
			[]string{"Bash(ls)", "Bash"}},
		"read names its absolute path from the root": {"Read", `{"file_path":"/w/src/a.go"}`, `[]`,
			[]string{"Read(//w/src/a.go)", "Read"}},
		"edit names its absolute path from the root": {"Edit", `{"file_path":"/w/a.go","old_string":"x","new_string":"y"}`, `[]`,
			[]string{"Edit(//w/a.go)", "Edit"}},
		"a relative path has no exact rule": {"Read", `{"file_path":"src/a.go"}`, `[]`,
			[]string{"Read"}},
		"a path with a glob character has no exact rule": {"Read", `{"file_path":"/w/[a].go"}`, `[]`,
			[]string{"Read"}},
		"a tool with no exact spelling offers the whole tool": {"WebFetch", `{"url":"https://example.com/x"}`, `[]`,
			[]string{"WebFetch"}},
		"an mcp tool is its own exact rule": {"mcp__srv__do_it", `{"a":1}`, `[]`,
			[]string{"mcp__srv__do_it"}},
		"a tool claude's syntax cannot name offers nothing of ours": {"9bad", `{}`, `[]`,
			nil},
	} {
		t.Run(name, func(t *testing.T) {
			ask, err := codec(t).DecodeAsk(hookEventPermissionRequest, permissionRequest(tc.tool, tc.input, tc.suggestions))
			require.NoError(t, err)
			assert.Equal(t, tc.want, ask.Suggestions)
		})
	}
}

// TestApprovalCodec_TheRulesItAddsAreRulesAndCoverTheCall: every rule the
// codec adds validates, and covers the very call it was offered for —
// granting it would never leave that call asking again.
func TestApprovalCodec_TheRulesItAddsAreRulesAndCoverTheCall(t *testing.T) {
	c := codec(t)
	for _, call := range []struct{ tool, input string }{
		{"Bash", `{"command":"go test ./... -run 'X (y)'"}`},
		{"Bash", `{"command":"echo (hi)"}`},
		{"Read", `{"file_path":"/home/u/.config/x y.json"}`},
		{"Edit", `{"file_path":"/a/b"}`},
		{"Glob", `{"pattern":"**/*.go"}`},
		{"mcp__ctxloom", `{}`},
	} {
		ask, err := c.DecodeAsk(hookEventPermissionRequest, permissionRequest(call.tool, call.input, `[]`))
		require.NoError(t, err)
		require.NotEmpty(t, ask.Suggestions, "%s %s", call.tool, call.input)
		for _, r := range ask.Suggestions {
			assert.NoErrorf(t, c.ValidateRule(r), "%q", r)
			assert.Truef(t, c.Covers(r, ask), "%q covers the call it was offered for: %s %s", r, call.tool, call.input)
		}
	}
}

func bashAsk(command string) engine.PermissionAsk {
	in, _ := json.Marshal(map[string]string{"command": command, "description": "anything"})
	return engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: in}
}

// TestApprovalCodec_Covers: a whole-tool rule covers every call of the tool
// (an MCP server's rule every tool of the server); an exact rule covers its
// one call; a rule whose reach the codec cannot judge exactly — a wildcard,
// a prefix, a relative or globbed path, a domain — covers nothing, so the
// call is asked about rather than allowed past what claude itself would.
func TestApprovalCodec_Covers(t *testing.T) {
	c := codec(t)
	read := func(p string) engine.PermissionAsk {
		return engine.PermissionAsk{Kind: engine.AskTool, Tool: "Read", Input: json.RawMessage(`{"file_path":"` + p + `"}`)}
	}
	mcp := engine.PermissionAsk{Kind: engine.AskTool, Tool: "mcp__srv__do_it", Input: json.RawMessage(`{}`)}
	for _, tc := range []struct {
		rule string
		ask  engine.PermissionAsk
		want bool
	}{
		{"Bash", bashAsk("rm -rf /tmp/x"), true},
		{"Bash(ls)", bashAsk("ls"), true},
		{"Bash(ls)", bashAsk("ls -la"), false},
		{"Bash(ls)", bashAsk("ls && rm x"), false},
		{"Bash(ls *)", bashAsk("ls -la"), false},
		{"Bash(ls:*)", bashAsk("ls -la"), false},
		{"Bash(*)", bashAsk("ls"), false},
		{"Read", bashAsk("ls"), false},
		{"Bash(ls)", read("/ls"), false},
		{"Read(//w/a.go)", read("/w/a.go"), true},
		{"Read(//w/a.go)", read("/w/b.go"), false},
		{"Read(/w/a.go)", read("/w/a.go"), false},
		{"Read(//w/*.go)", read("/w/a.go"), false},
		{"Read(./a.go)", read("a.go"), false},
		{"Edit(//w/a.go)", read("/w/a.go"), false},
		{"mcp__srv__do_it", mcp, true},
		{"mcp__srv", mcp, true},
		{"mcp__sr", mcp, false},
		{"mcp__srv__do", mcp, false},
		{"mcp__other", mcp, false},
		{"WebFetch(domain:example.com)", engine.PermissionAsk{Kind: engine.AskTool, Tool: "WebFetch", Input: json.RawMessage(`{"url":"https://example.com"}`)}, false},
		{"", bashAsk("ls"), false},
		{"Bash(", bashAsk("ls"), false},
		{"Bash", engine.PermissionAsk{Kind: engine.AskQuestion, Tool: "Bash"}, false},
		{"Bash(ls)", engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{`)}, false},
	} {
		assert.Equalf(t, tc.want, c.Covers(tc.rule, tc.ask), "Covers(%q, %s %s)", tc.rule, tc.ask.Tool, tc.ask.Input)
	}
}
