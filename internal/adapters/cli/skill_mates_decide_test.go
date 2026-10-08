package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
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

// skillUse is a main-thread transcript entry for one claude Skill tool call.
func skillUse(name string) agent.ChatEvent {
	return vendorreader.ToolUseEvent(skillTool, "t-"+name, json.RawMessage(`{"skill":"`+name+`","args":""}`))
}

// postTool decodes a claude PostToolUse payload for tool with input through
// claude's codec: the event the skill-mates decision is handed.
func postTool(t *testing.T, tool, input string) engine.HookEvent {
	t.Helper()
	ev, err := claudeCodec(t).Decode("post_tool", []byte(`{"hook_event_name":"PostToolUse","tool_name":"`+tool+`","tool_input":`+input+`}`))
	require.NoError(t, err)
	return ev
}

// skillCompleted is the post_tool event of a completed claude Skill call.
func skillCompleted(t *testing.T, name string) engine.HookEvent {
	return postTool(t, skillTool, `{"skill":"`+name+`"}`)
}

// (a) A member skill completes and its mate is uninvoked: the line names the
// mate.
//
// MUTATION -- return HookResponse{} unconditionally -- turns this red.
func TestSkillMatesResponse_MemberCompletesMateUninvoked_NamesTheMate(t *testing.T) {
	out := skillMatesResponse(claudeCodec(t), skillCompleted(t, "admit"), nightlyPair(), []agent.ChatEvent{skillUse("admit")})
	assert.Equal(t, skillMatesContext("admit", []string{"unattended"}), out.Context)
	assert.Contains(t, out.Context, "unattended")
}

// (b) Every mate was already invoked this session: nothing.
//
// MUTATION -- invert the membership test in skillsInvoked's returned set, or
// have skillMatesResponse pass a never-invoked predicate -- turns this red.
func TestSkillMatesResponse_AllMatesInvoked_Silent(t *testing.T) {
	prior := []agent.ChatEvent{skillUse("unattended"), skillUse("admit")}
	assert.True(t, skillMatesResponse(claudeCodec(t), skillCompleted(t, "admit"), nightlyPair(), prior).Empty())
}

// (c) A skill in no link group: nothing, however many linked skills sit
// beside it.
func TestSkillMatesResponse_NonMemberSkill_Silent(t *testing.T) {
	assert.True(t, skillMatesResponse(claudeCodec(t), skillCompleted(t, "free"), nightlyPair(), nil).Empty())
}

// (d) The completed tool is not the skill tool: nothing, even when its input
// happens to carry a "skill" key — the codec answers that no skill ran.
func TestSkillMatesResponse_NonSkillTool_Silent(t *testing.T) {
	ev := postTool(t, "Bash", `{"skill":"admit","command":"ls"}`)
	assert.Empty(t, ev.Skill)
	assert.True(t, skillMatesResponse(claudeCodec(t), ev, nightlyPair(), nil).Empty())
}

// (e) A skill in TWO groups names the uninvoked mates of both, once each.
//
// MUTATION -- stop after the first group's mates -- turns this red.
func TestSkillMatesResponse_SkillInTwoGroups_NamesBothGroupsMates(t *testing.T) {
	delivered := []*bundles.LoadedSkill{
		linkedSkill("closeout", "wrap", "review"),
		linkedSkill("prompt-human", "wrap"),
		linkedSkill("check-triggers", "review"),
	}
	out := skillMatesResponse(claudeCodec(t), skillCompleted(t, "closeout"), delivered, nil)
	assert.Equal(t, skillMatesContext("closeout", []string{"check-triggers", "prompt-human"}), out.Context)

	out = skillMatesResponse(claudeCodec(t), skillCompleted(t, "closeout"), delivered, []agent.ChatEvent{skillUse("prompt-human")})
	assert.Equal(t, skillMatesContext("closeout", []string{"check-triggers"}), out.Context)
}

// "Already invoked" is derived from the transcript's own skill tool_use
// records on the MAIN thread, each asked of the codec. A subagent's
// invocation does not count: the owner session never saw that skill's body,
// which is the miss this hook exists to close.
//
// MUTATION -- drop the Sidechain check in skillsInvoked -- turns this red.
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
	invoked := skillsInvoked(claudeCodec(t), evs)
	assert.True(t, invoked("admit"))
	assert.False(t, invoked("unattended"), "a sidechain invocation is not the owner session's")
	assert.False(t, invoked("Bash"))
}

// The line is factual and names only: the completed skill and its uninvoked
// mates. No mates means no line at all -- silence is the common case and an
// empty-but-present line would still be an attachment.
//
// MUTATION -- return the format with an empty list for nil mates -- turns
// this red.
func TestSkillMatesContext_NamesOnlyAndSilentForNoMates(t *testing.T) {
	line := skillMatesContext("admit", []string{"unattended", "prompt-human"})
	assert.Contains(t, line, "admit")
	assert.Contains(t, line, "unattended, prompt-human")
	assert.Equal(t, "", skillMatesContext("admit", nil))
}
