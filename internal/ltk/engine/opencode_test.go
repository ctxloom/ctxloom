package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bridge's payloads: bash is a command, edit and write a path, an
// apply_patch of one file that file; anything ltk cannot read whole is
// ungated (evaluate denies it).
func TestOpencodeDecode(t *testing.T) {
	cases := []struct {
		name, payload string
		want          Request
	}{
		{"bash", `{"event":"tool.execute.before","session":"s","tool":"bash","tool_input":{"command":"go test ./...","description":"d"}}`,
			Request{ToolName: "bash", Command: "go test ./..."}},
		{"edit", `{"event":"tool.execute.before","tool":"edit","tool_input":{"filePath":"/p/vendor/x.go","oldString":"a","newString":"b"}}`,
			Request{ToolName: "edit", FilePath: "/p/vendor/x.go"}},
		{"write", `{"tool":"write","tool_input":{"filePath":"/p/a.go","content":"x"}}`,
			Request{ToolName: "write", FilePath: "/p/a.go"}},
		{"one-file patch", `{"tool":"apply_patch","tool_input":{"patchText":"*** Begin Patch\n*** Update File: /p/a.go\n@@\n-a\n+b\n*** End Patch"}}`,
			Request{ToolName: "apply_patch", FilePath: "/p/a.go"}},
		{"two-file patch", `{"tool":"apply_patch","tool_input":{"patchText":"*** Add File: a\n+x\n*** Delete File: b\n"}}`,
			Request{ToolName: "apply_patch", ToolUngated: true}},
		{"unclassed tool", `{"tool":"webfetch","tool_input":{"url":"u"}}`,
			Request{ToolName: "webfetch", ToolUngated: true}},
		{"a claude payload", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`,
			Request{ToolUngated: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Opencode{}.Decode([]byte(c.payload))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
	_, err := Opencode{}.Decode([]byte("not json"))
	require.Error(t, err)
}

// A deny is the bridge's blocking answer carrying the message; an allow is
// silent.
func TestOpencodeEncode(t *testing.T) {
	out, err := Opencode{}.Encode(Response{Reason: "Use just test.", Suggest: "just test"})
	require.NoError(t, err)
	var a struct {
		Block  bool   `json:"block"`
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(out.Stdout, &a))
	assert.True(t, a.Block)
	assert.Equal(t, "Use just test.\n\nUse instead: just test", a.Reason)
	assert.Zero(t, out.ExitCode)

	out, err = Opencode{}.Encode(Response{Allow: true})
	require.NoError(t, err)
	assert.Equal(t, Output{}, out)
}

// opencode decodes at runtime but is no engine `manage` installs into.
func TestOpencodeIsRuntimeOnly(t *testing.T) {
	a, err := AdapterFor("opencode")
	require.NoError(t, err)
	assert.IsType(t, Opencode{}, a)
	_, err = Get("opencode")
	require.Error(t, err, "manage cannot resolve it")
	assert.Len(t, Adapters(), len(All())+1)
	_, err = AdapterFor("nope")
	require.Error(t, err)
}
