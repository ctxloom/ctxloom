package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// linkedSkill is a delivered skill as the loader emits it, tagged into the
// named link groups.
func linkedSkill(name string, linkIDs ...string) *bundles.LoadedSkill {
	tags := make([]string, 0, len(linkIDs))
	for _, id := range linkIDs {
		tags = append(tags, "ctxloom:link_id="+id)
	}
	return &bundles.LoadedSkill{
		Name: "ops/" + name, Bundle: "ops", Item: name,
		Frontmatter: bundles.SkillFrontmatter{Name: name},
		Tags:        tags,
	}
}

// nightlyPair is the corpus's measured miss: admit -> unattended.
func nightlyPair() []*bundles.LoadedSkill {
	return []*bundles.LoadedSkill{
		linkedSkill("admit", "nightly"),
		linkedSkill("unattended", "nightly"),
		linkedSkill("free"),
	}
}

// skillUse is a main-thread transcript entry for one Skill tool call.
func skillUse(name string) agent.ChatEvent {
	return vendorreader.ToolUseEvent(SkillToolName, "t-"+name, json.RawMessage(`{"skill":"`+name+`","args":""}`))
}

func skillCompleted(name string) PostToolUsePayload {
	return PostToolUsePayload{ToolName: SkillToolName, ToolInput: json.RawMessage(`{"skill":"` + name + `"}`)}
}

func contextOf(out PostToolUseOutput) string {
	if out.HookSpecificOutput == nil {
		return ""
	}
	return out.HookSpecificOutput.AdditionalContext
}

// (a) A member skill completes and its mate is uninvoked: the line names the
// mate, and rides the PostToolUse event so the engine attaches it.
//
// MUTATION -- return PostToolUseOutput{} unconditionally -- turns this red.
func TestBuildSkillMatesOutput_MemberCompletesMateUninvoked_NamesTheMate(t *testing.T) {
	out := BuildSkillMatesOutput(skillCompleted("admit"), nightlyPair(), []agent.ChatEvent{skillUse("admit")})

	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, HookEventPostToolUse, out.HookSpecificOutput.HookEventName)
	assert.Equal(t, SkillMatesContext("admit", []string{"unattended"}), out.HookSpecificOutput.AdditionalContext)
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, "unattended")
}

// (b) Every mate was already invoked this session: nothing.
//
// MUTATION -- invert the membership test in SkillsInvoked's returned set, or
// have BuildSkillMatesOutput pass a never-invoked predicate -- turns this red.
func TestBuildSkillMatesOutput_AllMatesInvoked_Silent(t *testing.T) {
	prior := []agent.ChatEvent{skillUse("unattended"), skillUse("admit")}
	out := BuildSkillMatesOutput(skillCompleted("admit"), nightlyPair(), prior)
	assert.Nil(t, out.HookSpecificOutput)
}

// (c) A skill in no link group: nothing, however many linked skills sit
// beside it.
func TestBuildSkillMatesOutput_NonMemberSkill_Silent(t *testing.T) {
	out := BuildSkillMatesOutput(skillCompleted("free"), nightlyPair(), nil)
	assert.Nil(t, out.HookSpecificOutput)
}

// (d) The completed tool is not Skill: nothing, even when its input happens to
// carry a "skill" key.
//
// MUTATION -- drop the ToolName == SkillToolName check in InvokedSkill --
// turns this red.
func TestBuildSkillMatesOutput_NonSkillTool_Silent(t *testing.T) {
	payload := PostToolUsePayload{ToolName: "Bash", ToolInput: json.RawMessage(`{"skill":"admit","command":"ls"}`)}
	out := BuildSkillMatesOutput(payload, nightlyPair(), nil)
	assert.Nil(t, out.HookSpecificOutput)
}

// (e) A skill in TWO groups names the uninvoked mates of both, once each.
//
// MUTATION -- stop after the first group's mates -- turns this red.
func TestBuildSkillMatesOutput_SkillInTwoGroups_NamesBothGroupsMates(t *testing.T) {
	delivered := []*bundles.LoadedSkill{
		linkedSkill("closeout", "wrap", "review"),
		linkedSkill("prompt-human", "wrap"),
		linkedSkill("check-triggers", "review"),
	}
	out := BuildSkillMatesOutput(skillCompleted("closeout"), delivered, nil)
	assert.Equal(t, SkillMatesContext("closeout", []string{"check-triggers", "prompt-human"}), contextOf(out))

	out = BuildSkillMatesOutput(skillCompleted("closeout"), delivered, []agent.ChatEvent{skillUse("prompt-human")})
	assert.Equal(t, SkillMatesContext("closeout", []string{"check-triggers"}), contextOf(out))
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
