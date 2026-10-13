package opencode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// THE HOOK WIRE (ruling #28): ctxloom's cross-engine command-hook wire is
// claude's PreToolUse. The bridge writes claude's input to every hook on
// opencode's pre-tool point, with opencode's tool ids and input keys
// normalised to claude's; the only reply it acts on is a deny with a reason.
// These tests drive the GENERATED plugin under node, as opencode would load
// it, with a stub hook that records its stdin and replies from a file.

// The normalisation table, pinned: opencode 1.18.35's tool ids to claude's
// names, and the input keys each renames. apply_patch has no claude name and
// keeps its own; a tool the table does not list passes verbatim.
func TestClaudeTools_TheNormalisationTable(t *testing.T) {
	assert.Equal(t, map[string]claudeTool{
		"bash":        {Name: "Bash"},
		"edit":        {Name: "Edit", Keys: map[string]string{"filePath": "file_path", "oldString": "old_string", "newString": "new_string", "replaceAll": "replace_all"}},
		"write":       {Name: "Write", Keys: map[string]string{"filePath": "file_path"}},
		"read":        {Name: "Read", Keys: map[string]string{"filePath": "file_path"}},
		"glob":        {Name: "Glob"},
		"grep":        {Name: "Grep", Keys: map[string]string{"include": "glob"}},
		"webfetch":    {Name: "WebFetch"},
		"websearch":   {Name: "WebSearch"},
		"task":        {Name: "Task"},
		"todowrite":   {Name: "TodoWrite"},
		"skill":       {Name: "Skill", Keys: map[string]string{"name": "skill"}},
		"apply_patch": {Name: "apply_patch"},
	}, claudeTools)
}

// A bundle hook narrowed to a tool class is bound to the CLAUDE names of
// opencode's tools in that class: the bridge matches on the normalised name.
func TestToolMatcher_SpellsAClassInClaudesNames(t *testing.T) {
	for class, want := range map[wire.ToolClass]string{
		wire.ToolShell:    "Bash",
		wire.ToolFileEdit: "Edit|Write|apply_patch",
		wire.ToolSkill:    "Skill",
	} {
		got, ok := toolMatcher(class)
		assert.True(t, ok, class)
		assert.Equal(t, want, got, class)
	}
	_, ok := toolMatcher("telepathy")
	assert.False(t, ok)
}

// wireRun is one pre-tool call through the generated bridge: what the hook
// read on stdin (nil when it never fired), the error the bridge threw ("" when
// the call went ahead), and the tool's args as opencode holds them after.
type wireRun struct {
	Payload map[string]any
	Thrown  string
	Args    map[string]any
}

// hookStub is a hook command recording its stdin to payload.json and
// answering with reply (and exit code) from the run's directory.
const hookStub = `cat > "$WIRE_DIR/payload.json"; cat "$WIRE_DIR/reply"; exit "$(cat "$WIRE_DIR/code")"`

// runPreTool renders the bridge for hooks, loads it in node and fires its
// tool.execute.before once for (tool, args), the stub answering reply/code.
func runPreTool(t *testing.T, hooks wire.UnifiedHooks, tool string, args map[string]any, reply string, code int) wireRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("the hook wire's contract runs the bridge under node, which CI must have")
		}
		t.Skip("node is not installed: the bridge's wire contract cannot run here")
	}
	dir := t.TempDir()
	src, err := bridgePlugin(hooks, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom.mjs"), src, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reply"), []byte(reply), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "code"), []byte(strconv.Itoa(code)), 0o644))
	driver := `import { CtxloomBridge } from "./ctxloom.mjs"
const [tool, args] = JSON.parse(process.argv[2])
const hooks = await CtxloomBridge({ client: { tui: { showToast: async () => {} }, session: {} } })
const output = { args }
let thrown = ""
try { await hooks["tool.execute.before"]({ tool, sessionID: "ses_1", callID: "call_1" }, output) } catch (e) { thrown = e.message || "(empty)" }
console.log(JSON.stringify({ thrown, args: output.args }))
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(driver), 0o644))
	in, err := json.Marshal([]any{tool, args})
	require.NoError(t, err)
	cmd := exec.Command(node, "driver.mjs", string(in))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "WIRE_DIR="+dir, "XDG_DATA_HOME="+filepath.Join(dir, "data"), wake.EnvVar+"=")
	out, err := cmd.Output()
	require.NoError(t, err, "node: %s", out)
	var res struct {
		Thrown string         `json:"thrown"`
		Args   map[string]any `json:"args"`
	}
	require.NoError(t, json.Unmarshal(out, &res), "driver output %s", out)
	run := wireRun{Thrown: res.Thrown, Args: res.Args}
	if b, err := os.ReadFile(filepath.Join(dir, "payload.json")); err == nil {
		require.NoError(t, json.Unmarshal(b, &run.Payload), "the hook's stdin %s", b)
		for _, k := range []string{"cwd", "transcript_path"} {
			if v, ok := run.Payload[k].(string); ok {
				run.Payload[k] = strings.Replace(v, dir, "<dir>", 1)
			}
		}
	}
	return run
}

func everyTool(cmd string) wire.UnifiedHooks {
	return wire.UnifiedHooks{PreTool: []wire.Hook{{Command: cmd}}}
}

// THE INPUT: claude's PreToolUse JSON, the tool's name and input keys in
// claude's spelling, opencode's own args left as opencode holds them.
func TestBridge_PreToolWritesClaudesPreToolUse(t *testing.T) {
	args := map[string]any{"filePath": "/p/a.go", "oldString": "x", "newString": "y", "replaceAll": true}
	run := runPreTool(t, everyTool(hookStub), "edit", args, "", 0)
	assert.Equal(t, map[string]any{
		"session_id":      "ses_1",
		"transcript_path": "<dir>/data/opencode/ctxloom/ses_1.jsonl",
		"cwd":             "<dir>",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": "/p/a.go", "old_string": "x", "new_string": "y", "replace_all": true},
		"tool_use_id":     "call_1",
	}, run.Payload)
	assert.Empty(t, run.Thrown, "an empty reply passes the call through")
	assert.Equal(t, args, run.Args, "opencode's own args are not rewritten")
}

// Each tool by the table: renamed keys renamed, every other key kept; a tool
// the table does not list (an MCP tool) passes name and input verbatim.
func TestBridge_PreToolNormalisesEachTool(t *testing.T) {
	for _, c := range []struct {
		tool  string
		args  map[string]any
		name  string
		input map[string]any
	}{
		{"bash", map[string]any{"command": "git status", "description": "d", "workdir": "/w"}, "Bash", map[string]any{"command": "git status", "description": "d", "workdir": "/w"}},
		{"write", map[string]any{"filePath": "/p/b", "content": "c"}, "Write", map[string]any{"file_path": "/p/b", "content": "c"}},
		{"grep", map[string]any{"pattern": "x", "include": "*.go"}, "Grep", map[string]any{"pattern": "x", "glob": "*.go"}},
		{"skill", map[string]any{"name": "closeout"}, "Skill", map[string]any{"skill": "closeout"}},
		{"apply_patch", map[string]any{"patchText": "*** Begin Patch"}, "apply_patch", map[string]any{"patchText": "*** Begin Patch"}},
		{"github_create_issue", map[string]any{"filePath": "kept"}, "github_create_issue", map[string]any{"filePath": "kept"}},
	} {
		t.Run(c.tool, func(t *testing.T) {
			run := runPreTool(t, everyTool(hookStub), c.tool, c.args, "", 0)
			assert.Equal(t, c.name, run.Payload["tool_name"])
			assert.Equal(t, c.input, run.Payload["tool_input"])
		})
	}
}

// THE REPLIES: a deny with a reason blocks the call with that reason; every
// other answer passes it through (allow, ask, a reasonless deny, the legacy
// decision block, garbage, a non-zero exit, exit 2 included).
func TestBridge_PreToolActsOnlyOnDenyWithReason(t *testing.T) {
	deny := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"use just test"}}`
	run := runPreTool(t, everyTool(hookStub), "bash", map[string]any{"command": "go test"}, deny, 0)
	assert.Equal(t, "use just test", run.Thrown)

	for name, c := range map[string]struct {
		reply string
		code  int
	}{
		"allow":          {`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`, 0},
		"ask":            {`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"sure?"}}`, 0},
		"reasonless":     {`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`, 0},
		"legacy block":   {`{"decision":"block","reason":"no"}`, 0},
		"own wire block": {`{"block":true,"reason":"no"}`, 0},
		"garbage":        {`not json`, 0},
		"exit 2":         {deny, 2},
	} {
		t.Run(name, func(t *testing.T) {
			run := runPreTool(t, everyTool(hookStub), "bash", map[string]any{"command": "go test"}, c.reply, c.code)
			require.NotNil(t, run.Payload, "the hook fired")
			assert.Empty(t, run.Thrown, "passes through")
		})
	}
}

// MATCHERS test the claude name, by claude's rule: a plain name list is
// exact, anything else an unanchored regex; a class binds to its claude names.
func TestBridge_PreToolMatchesOnClaudesNames(t *testing.T) {
	for _, c := range []struct {
		hook  wire.Hook
		tool  string
		fires bool
	}{
		{wire.Hook{Command: hookStub, Tool: wire.ToolShell}, "bash", true},
		{wire.Hook{Command: hookStub, Tool: wire.ToolShell}, "edit", false},
		{wire.Hook{Command: hookStub, Tool: wire.ToolFileEdit}, "apply_patch", true},
		{wire.Hook{Command: hookStub, Matcher: "Edit|Write"}, "write", true},
		{wire.Hook{Command: hookStub, Matcher: "Edi"}, "edit", false},
		{wire.Hook{Command: hookStub, Matcher: "bash"}, "bash", false},
		{wire.Hook{Command: hookStub, Matcher: "^Web"}, "webfetch", true},
		{wire.Hook{Command: hookStub, Matcher: "*"}, "glob", true},
	} {
		t.Run(c.hook.Matcher+string(c.hook.Tool)+"/"+c.tool, func(t *testing.T) {
			run := runPreTool(t, wire.UnifiedHooks{PreTool: []wire.Hook{c.hook}}, c.tool, map[string]any{}, "", 0)
			assert.Equal(t, c.fires, run.Payload != nil)
		})
	}
}
