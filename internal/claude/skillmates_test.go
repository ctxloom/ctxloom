package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// skillUse is a main-thread transcript entry for one Skill tool call.
func skillUse(name string) agent.ChatEvent {
	return vendorreader.ToolUseEvent(SkillToolName, "t-"+name, json.RawMessage(`{"skill":"`+name+`","args":""}`))
}

// "Already invoked" is derived from the transcript's own Skill tool_use records
// on the MAIN thread. A subagent's invocation does not count: the owner
// session never saw that skill's body, which is the miss this hook exists to
// close.
//
// MUTATION -- drop the Sidechain check in SkillsInvoked -- turns this red.
func TestSkillsInvoked_ReadsMainThreadSkillToolUsesOnly(t *testing.T) {
	subagent := skillUse("unattended")
	subagent.Entry.Sidechain = true
	evs := []agent.ChatEvent{
		skillUse("admit"),
		subagent,
		vendorreader.ToolUseEvent("Bash", "t-bash", json.RawMessage(`{"command":"ls"}`)),
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "text"}},
		{Complete: &agent.TurnMeta{}},
	}
	invoked := SkillsInvoked(evs)
	assert.True(t, invoked("admit"))
	assert.False(t, invoked("unattended"), "a sidechain invocation is not the owner session's")
	assert.False(t, invoked("Bash"))
}

// InvokedSkill decodes the Skill tool's input as claude-code sends it; an
// undecodable or empty input is no invocation.
func TestInvokedSkill_DecodesTheSkillToolInput(t *testing.T) {
	name, ok := InvokedSkill(SkillToolName, json.RawMessage(`{"skill":"closeout","args":"--fast"}`))
	assert.True(t, ok)
	assert.Equal(t, "closeout", name)

	for _, input := range []string{``, `not json`, `{}`, `{"skill":""}`} {
		_, ok := InvokedSkill(SkillToolName, json.RawMessage(input))
		assert.False(t, ok, "input %q", input)
	}
	_, ok = InvokedSkill("Read", json.RawMessage(`{"skill":"closeout"}`))
	assert.False(t, ok)
}

// The line is factual and names only: the completed skill and its uninvoked
// mates. No mates means no line at all -- silence is the common case and an
// empty-but-present line would still be an attachment.
//
// MUTATION -- return the format with an empty list for nil mates -- turns
// this red.
func TestSkillMatesContext_NamesOnlyAndSilentForNoMates(t *testing.T) {
	line := SkillMatesContext("admit", []string{"unattended", "prompt-human"})
	assert.Contains(t, line, "admit")
	assert.Contains(t, line, "unattended, prompt-human")
	assert.Equal(t, "", SkillMatesContext("admit", nil))
}

// The payload decodes the fields claude-code sends a PostToolUse hook that this
// verb needs: the tool name, its raw input, and the transcript path the
// "already invoked" derivation reads.
func TestPostToolUsePayload_CarriesToolInputAndTranscriptPath(t *testing.T) {
	var p PostToolUsePayload
	require.NoError(t, json.Unmarshal([]byte(`{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/repo","tool_name":"Skill","tool_input":{"skill":"admit"},"tool_response":"ok"}`), &p))
	assert.Equal(t, "/t.jsonl", p.TranscriptPath)
	assert.Equal(t, "/repo", p.Cwd)
	name, ok := InvokedSkill(p.ToolName, p.ToolInput)
	assert.True(t, ok)
	assert.Equal(t, "admit", name)
}
