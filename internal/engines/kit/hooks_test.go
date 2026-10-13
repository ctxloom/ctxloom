package kit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

var testEvents = HookEvents{{Unified: "pre_tool", Native: "before"}, {Unified: "turn_start", Native: "message"}}

func TestHookEvents_MapsBothWays(t *testing.T) {
	assert.Equal(t, "pre_tool", testEvents.Unified("before"), "a native name maps to its unified one")
	assert.Equal(t, "turn_start", testEvents.Unified("turn_start"), "a unified name is itself")
	assert.Equal(t, "other", testEvents.Unified("other"), "an unlisted name is carried verbatim")
	assert.Equal(t, map[string]string{"pre_tool": "before", "turn_start": "message"}, testEvents.Map())
}

// The codec decodes every field of ctxloom's own payload, maps the event
// through the engine's table, and asks the engine which skill and file the
// tool call touched.
func TestNativeHookCodec_DecodesItsOwnWire(t *testing.T) {
	c := NativeHookCodec{
		Engine:     "e",
		Events:     testEvents,
		Skill:      func(tool string, _ []byte) (string, bool) { return "s", tool == "skill" },
		EditedPath: func(tool string, _ []byte) string { return tool + ".go" },
	}
	ev, err := c.Decode("ignored", []byte(`{"event":"before","session":"k","transcript":"/t","source":"resume","prompt":"p","tool":"skill","tool_input":{"a":1},"tool_response":"r"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{
		Event: "pre_tool", NativeSession: "k", Transcript: "/t", Source: "resume", Prompt: "p",
		Tool: "skill", ToolInput: []byte(`{"a":1}`), ToolResponse: []byte(`"r"`), Skill: "s", Path: "skill.go",
	}, ev)

	ev, err = c.Decode("message", []byte(`{"session":"k"}`))
	require.NoError(t, err)
	assert.Equal(t, "turn_start", ev.Event, "a payload naming no event takes the registered one")

	_, err = c.Decode("pre_tool", []byte("not json"))
	require.Error(t, err)
}

func TestNativeHookCodec_EncodesOnlyWhatTheEventCarries(t *testing.T) {
	open := NativeHookCodec{Engine: "e"}
	reply, err := open.Encode("turn_end", engine.HookResponse{Context: "c", Notice: "n", Block: true, Reason: "r"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"context":"c","notice":"n","block":true,"reason":"r"}`, string(reply.Stdout))
	assert.Zero(t, reply.Exit)

	narrow := NativeHookCodec{Engine: "e", ContextOn: []string{"turn_start"}, BlockOn: []string{"pre_tool"}}
	_, err = narrow.Encode("turn_end", engine.HookResponse{Context: "c"})
	require.ErrorContains(t, err, "no hook context on turn_end")
	_, err = narrow.Encode("turn_end", engine.HookResponse{Block: true})
	require.ErrorContains(t, err, "cannot block turn_end")
	_, err = narrow.Encode("turn_start", engine.HookResponse{Context: "c", Notice: "n"})
	require.NoError(t, err)
	_, err = narrow.Encode("pre_tool", engine.HookResponse{Block: true, Reason: "r"})
	require.NoError(t, err)
}

func TestNativeHookCodec_NoSkillToolNamesNoSkill(t *testing.T) {
	_, ok := NativeHookCodec{}.InvokedSkill("skill", []byte(`"x"`))
	assert.False(t, ok)
	assert.Equal(t, 7, NativeHookCodec{Limit: 7}.ContextLimit())
}
