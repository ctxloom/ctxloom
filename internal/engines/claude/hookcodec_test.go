package claude

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A PostToolUse payload decodes into every neutral field the post_tool verbs
// read: the tool, its raw input and response, the transcript, and the skill
// a Skill call ran.
//
// MUTATION -- drop ToolResponse (or Skill) from Decode's HookEvent -- turns
// this red.
func TestHookCodec_DecodesAPostToolUsePayload(t *testing.T) {
	ev, err := hookCodec{}.Decode(wire.HookEventPostTool, []byte(`{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/repo","hook_event_name":"PostToolUse","tool_name":"Skill","tool_input":{"skill":"admit"},"tool_response":"ok"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{
		Event: wire.HookEventPostTool, NativeSession: "s", Transcript: "/t.jsonl",
		Tool: "Skill", ToolInput: []byte(`{"skill":"admit"}`), ToolResponse: []byte(`"ok"`), Skill: "admit",
	}, ev)
}

// A SessionStart payload carries why the session started; an editing tool's
// payload carries the file it targeted, under file_path or (NotebookEdit)
// notebook_path.
func TestHookCodec_DecodesSourceAndEditedPath(t *testing.T) {
	ev, err := hookCodec{}.Decode(wire.HookEventSessionStart, []byte(`{"session_id":"s","transcript_path":"/t.jsonl","source":"clear"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{Event: wire.HookEventSessionStart, NativeSession: "s", Transcript: "/t.jsonl", Source: engine.SessionSourceClear}, ev)

	for input, want := range map[string]string{
		`{"file_path":"/p/plan.md"}`:     "/p/plan.md",
		`{"notebook_path":"/p/n.ipynb"}`: "/p/n.ipynb",
		`{"command":"ls"}`:               "",
		`"not an object"`:                "",
	} {
		ev, err := hookCodec{}.Decode(wire.HookEventPostFileEdit, []byte(`{"tool_name":"Edit","tool_input":`+input+`}`))
		require.NoError(t, err)
		assert.Equal(t, want, ev.Path, "input %s", input)
		assert.Equal(t, wire.HookEventPostFileEdit, ev.Event, "with no hook_event_name the registration's event stands")
	}
}

// A payload that is not claude's JSON is an error, never an empty event.
func TestHookCodec_RefusesAnUndecodablePayload(t *testing.T) {
	_, err := hookCodec{}.Decode(wire.HookEventTurnStart, []byte("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), EngineName)
}

// Encode renders each neutral response as the envelope claude reads, with the
// native event name claude requires inside hookSpecificOutput.
//
// MUTATION -- map turn_start to "SessionStart" in contextEvents, or drop the
// systemMessage -- turns this red.
func TestHookCodec_EncodesTheEnvelopeClaudeReads(t *testing.T) {
	for _, c := range []struct {
		event string
		r     engine.HookResponse
		want  string
	}{
		{wire.HookEventSessionStart, engine.HookResponse{Context: "essence", Notice: "run /recover"},
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"essence"},"systemMessage":"run /recover"}`},
		{wire.HookEventTurnStart, engine.HookResponse{Context: "mail"},
			`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"mail"}}`},
		{wire.HookEventTurnStart, engine.HookResponse{Block: true, Reason: "stale wake"},
			`{"decision":"block","reason":"stale wake"}`},
		{wire.HookEventPostTool, engine.HookResponse{Context: "reflect"},
			`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"reflect"}}`},
		{wire.HookEventPostTool, engine.HookResponse{}, `{}`},
	} {
		got, err := hookCodec{}.Encode(c.event, c.r)
		require.NoError(t, err, c.event)
		assert.JSONEq(t, c.want, string(got.Stdout), c.event)
		assert.Equal(t, 0, got.Exit, "claude reads a non-zero exit as the hook failing")
	}
}

// A response claude has no native form for on the event is refused, not
// written into an envelope claude would reject or ignore.
func TestHookCodec_RefusesWhatClaudeCannotCarry(t *testing.T) {
	_, err := hookCodec{}.Encode(wire.HookEventPreTool, engine.HookResponse{Context: "x"})
	require.Error(t, err, "PreToolUse carries no additionalContext envelope here")
	_, err = hookCodec{}.Encode(wire.HookEventSessionStart, engine.HookResponse{Block: true})
	require.Error(t, err, "a SessionStart hook cannot block")
}

// InvokedSkill answers from the Skill tool's input alone; an undecodable or
// empty input, or another tool, is no invocation.
func TestHookCodec_InvokedSkill(t *testing.T) {
	name, ok := hookCodec{}.InvokedSkill(skillToolName, []byte(`{"skill":"closeout","args":"--fast"}`))
	assert.True(t, ok)
	assert.Equal(t, "closeout", name)
	for _, input := range []string{``, `not json`, `{}`, `{"skill":""}`} {
		_, ok := hookCodec{}.InvokedSkill(skillToolName, []byte(input))
		assert.False(t, ok, "input %q", input)
	}
	_, ok = hookCodec{}.InvokedSkill("Read", []byte(`{"skill":"closeout"}`))
	assert.False(t, ok)
	assert.Equal(t, additionalContextMaxChars, hookCodec{}.ContextLimit())
}

// Delivering the hooks binds them to claude: a ctxloom callback names claude
// as the engine that fires it, and the skill-mates hook narrowed to the skill
// tool class registers under claude's Skill matcher.
//
// MUTATION -- drop the BindHooks call from DeliverHooks -- turns this red.
func TestHooks_DeliveryBindsCallbacksAndToolClassesToClaude(t *testing.T) {
	eng, err := Build()
	require.NoError(t, err)
	start, project, _ := hostStart(t)
	hooks := wire.UnifiedHooks{PostTool: []wire.Hook{agent.NewSkillMatesHook()}}
	d, err := eng.Root().Hooks.DeliverHooks(start, present.RootProjectRoot, engine.HooksInputs{Hooks: hooks}, nil)
	require.NoError(t, err)
	var found bool
	for _, c := range d.Claims[filepath.Join(project, ".claude", "settings.json")] {
		v, ok := c.Value.(map[string]any)
		if !ok {
			continue
		}
		raw, _ := json.Marshal(v["args"])
		var args []string
		require.NoError(t, json.Unmarshal(raw, &args))
		if !slices.Contains(args, "skill-mates") {
			continue
		}
		found = true
		assert.Equal(t, []string{"hook", "skill-mates", agent.HookEngineFlag, EngineName}, args)
		assert.Contains(t, c.Pointer, present.PointerSelect("matcher", skillToolName))
	}
	require.True(t, found, "the skill-mates hook was not delivered")
}
