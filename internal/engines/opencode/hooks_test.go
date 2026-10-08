package opencode

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// goldenHooks is a hook set exercising every arm of the bridge's config: a
// ctxloom callback on each carried point (bound: --engine opencode), the ltk
// guards narrowed to tool classes, an unnarrowed pre_shell and post_file_edit
// (narrowed to their class on the broad point), a native matcher kept as
// given, a prompt hook (no opencode form), and the two lost events.
func goldenHooks() (wire.UnifiedHooks, wire.BackendHooks) {
	cb := func(verb string) wire.Hook {
		return wire.Hook{Command: agent.CtxloomCommand(), Args: []string{"hook", verb}, Timeout: 5}
	}
	u := wire.UnifiedHooks{
		SessionStart:  []wire.Hook{cb("session-bind")},
		TurnStart:     []wire.Hook{cb("mail-drain")},
		TurnEnd:       []wire.Hook{cb("next-step")},
		PreTool:       []wire.Hook{{Command: "ltk evaluate", Tool: wire.ToolShell}, {Command: "ltk evaluate", Tool: wire.ToolFileEdit}, {Command: "guard", Matcher: "^webfetch$"}},
		PreShell:      []wire.Hook{{Command: "shell-only"}},
		PostTool:      []wire.Hook{cb("skill-mates"), {Type: "prompt", Prompt: "no opencode form"}},
		PostFileEdit:  []wire.Hook{{Command: "fmt-edited"}},
		SessionEnd:    []wire.Hook{{Command: "lost-end"}},
		PermissionAsk: []wire.Hook{{Command: "lost-ask"}},
	}
	u.PostTool[0].Tool = wire.ToolSkill
	return u, wire.BackendHooks{"tool.execute.before": {{Command: "native-before"}}, "unknown.point": {{Command: "dropped"}}}
}

// THE BRIDGE, pinned whole: the generated plugin is the JS template with the
// config spliced in. A change to either shows here as a diff to review.
// Regenerate with CTXLOOM_UPDATE_GOLDEN=<this package's directory> (the
// test runs in a sandboxed working directory, so the golden's home is named).
func TestBridgePlugin_Golden(t *testing.T) {
	u, ext := goldenHooks()
	got, err := bridgePlugin(u, ext)
	require.NoError(t, err)
	got = []byte(strings.ReplaceAll(string(got), agent.CtxloomCommand(), "ctxloom"))
	if dir := os.Getenv("CTXLOOM_UPDATE_GOLDEN"); dir != "" {
		path := filepath.Join(dir, "testdata", "bridge.golden.js")
		require.NoError(t, os.WriteFile(path, got, 0o644))
		t.Fatalf("golden %s rewritten; re-run without CTXLOOM_UPDATE_GOLDEN", path)
	}
	assert.Equal(t, bridgeGolden, string(got))
}

// The config's semantics, independent of the golden's bytes: every carried
// event is filed under its point, narrowed events ride the broad point behind
// their class's matcher, callbacks name opencode, lost events and hooks with
// no opencode form are dropped, and only a known native point passes through.
func TestBridgePlugin_FilesEachHookUnderItsPoint(t *testing.T) {
	u, ext := goldenHooks()
	src, err := bridgePlugin(u, ext)
	require.NoError(t, err)
	cfg := bridgeConfigOf(t, src)
	commands := func(point string) []string {
		var out []string
		for _, h := range cfg.Hooks[point] {
			out = append(out, h.Command+" "+strings.Join(h.Args, " ")+" ~"+h.Matcher)
		}
		return out
	}
	cl := agent.CtxloomCommand()
	assert.Equal(t, []string{cl + " hook session-bind --engine opencode ~"}, commands("session.created"))
	assert.Equal(t, []string{cl + " hook mail-drain --engine opencode ~"}, commands("chat.message"))
	assert.Equal(t, []string{cl + " hook next-step --engine opencode ~"}, commands("session.idle"))
	assert.Equal(t, []string{"ltk evaluate  ~^(bash)$", "ltk evaluate  ~^(edit|write|apply_patch)$", "guard  ~^webfetch$", "shell-only  ~^(bash)$", "native-before  ~"}, commands("tool.execute.before"))
	assert.Equal(t, []string{cl + " hook skill-mates --engine opencode ~^(skill)$", "fmt-edited  ~^(edit|write|apply_patch)$"}, commands("tool.execute.after"))
	assert.Len(t, cfg.Hooks, 5, "no point beyond the bridge's five")
	assert.Equal(t, wake.EnvVar, cfg.WakeEnv)
	assert.Equal(t, "XDG_DATA_HOME", cfg.Transcript.Env)
	assert.Equal(t, "opencode/ctxloom", cfg.Transcript.Rel)
	for _, h := range u.PreShell {
		assert.Empty(t, h.Tool, "the caller's set is not mutated")
	}
}

func TestBridgePlugin_RefusesAClassOpencodeHasNoToolFor(t *testing.T) {
	_, err := bridgePlugin(wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "x", Tool: "telepathy"}}}, nil)
	require.ErrorContains(t, err, `opencode has no "telepathy" tool`)
}

// The hooks approach writes the bridge under H/plugins, announced on
// OPENCODE_CONFIG_DIR — even with no hook to fire, since the wake and the
// transcript ride it.
func TestDeliverHooks_WritesTheBridge(t *testing.T) {
	st, _, home := start(t)
	mem := afero.NewMemMapFs()
	d, err := def(t).Hooks.DeliverHooks(st, present.RootSessionHome, engine.HooksInputs{}, mem)
	require.NoError(t, err)
	file := filepath.Join(home, "plugins", "ctxloom.js")
	assert.Equal(t, []string{file}, d.Files)
	assert.Equal(t, home, d.Presented.Env[envConfigDir])
	b, err := afero.ReadFile(mem, file)
	require.NoError(t, err)
	assert.Contains(t, string(b), "export const CtxloomBridge")
	assert.NotContains(t, string(b), configMarker, "the config is spliced in")

	_, err = def(t).Hooks.DeliverHooks(st, present.RootProjectRoot, engine.HooksInputs{}, mem)
	require.Error(t, err, "the bridge lands in the session home only")
}

// opencode's codec over payloads as the bridge writes them: the point maps
// to the unified event, the skill tool names its skill, edit and write name
// their file; context and blocks only where the bridge can carry them.
func TestHookCodec_ReadsTheBridgesPayloads(t *testing.T) {
	ev, err := hookCodec.Decode("pre_tool", []byte(`{"event":"tool.execute.before","session":"ses_1","tool":"edit","tool_input":{"filePath":"/p/a.go"}}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{Event: "pre_tool", NativeSession: "ses_1", Tool: "edit", ToolInput: []byte(`{"filePath":"/p/a.go"}`), Path: "/p/a.go"}, ev)

	ev, err = hookCodec.Decode("post_tool", []byte(`{"event":"tool.execute.after","session":"s","tool":"skill","tool_input":{"name":"closeout"}}`))
	require.NoError(t, err)
	assert.Equal(t, "closeout", ev.Skill)
	assert.Empty(t, ev.Path)

	ev, err = hookCodec.Decode("session_start", []byte(`{"event":"session.created","session":"s","source":"startup","transcript":"/d/opencode/ctxloom/s.jsonl"}`))
	require.NoError(t, err)
	assert.Equal(t, "session_start", ev.Event)
	assert.Equal(t, "/d/opencode/ctxloom/s.jsonl", ev.Transcript)

	_, ok := hookCodec.InvokedSkill("bash", []byte(`{"name":"x"}`))
	assert.False(t, ok)
	assert.Empty(t, editedPath("apply_patch", []byte(`{"patchText":"x"}`)))

	_, err = hookCodec.Encode("turn_end", engine.HookResponse{Context: "c"})
	require.Error(t, err, "turn_end carries no context")
	_, err = hookCodec.Encode("session_start", engine.HookResponse{Block: true})
	require.Error(t, err, "session_start cannot be blocked")
	reply, err := hookCodec.Encode("turn_start", engine.HookResponse{Context: "mail", Block: true, Reason: "stale"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"context":"mail","block":true,"reason":"stale"}`, string(reply.Stdout))
}

func TestToolClass_MapsOpencodesToolIDs(t *testing.T) {
	for tool, want := range map[string]wire.ToolClass{"bash": wire.ToolShell, "edit": wire.ToolFileEdit, "write": wire.ToolFileEdit, "apply_patch": wire.ToolFileEdit, "skill": wire.ToolSkill} {
		got, ok := ToolClass(tool)
		assert.True(t, ok, tool)
		assert.Equal(t, want, got, tool)
	}
	_, ok := ToolClass("read")
	assert.False(t, ok)
}

//go:embed testdata/bridge.golden.js
var bridgeGolden string

// bridgeConfigOf reads back the config a generated bridge was spliced with.
func bridgeConfigOf(t *testing.T, src []byte) bridgeConfig {
	t.Helper()
	const decl = "const CONFIG = "
	i := strings.Index(string(src), decl)
	require.GreaterOrEqual(t, i, 0, "the bridge declares its config")
	line := string(src[i+len(decl):])
	line = line[:strings.IndexByte(line, '\n')]
	var cfg bridgeConfig
	require.NoError(t, json.Unmarshal([]byte(line), &cfg))
	return cfg
}
