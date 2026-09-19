package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
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
	return vendorreader.ToolUseEvent(claude.SkillToolName, "t-"+name, json.RawMessage(`{"skill":"`+name+`","args":""}`))
}

func skillCompleted(name string) claude.PostToolUsePayload {
	return claude.PostToolUsePayload{ToolName: claude.SkillToolName, ToolInput: json.RawMessage(`{"skill":"` + name + `"}`)}
}

func contextOf(out claude.PostToolUseOutput) string {
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
	out := buildSkillMatesOutput(skillCompleted("admit"), nightlyPair(), []agent.ChatEvent{skillUse("admit")})

	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, claude.HookEventPostToolUse, out.HookSpecificOutput.HookEventName)
	assert.Equal(t, claude.SkillMatesContext("admit", []string{"unattended"}), out.HookSpecificOutput.AdditionalContext)
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, "unattended")
}

// (b) Every mate was already invoked this session: nothing.
//
// MUTATION -- invert the membership test in SkillsInvoked's returned set, or
// have buildSkillMatesOutput pass a never-invoked predicate -- turns this red.
func TestBuildSkillMatesOutput_AllMatesInvoked_Silent(t *testing.T) {
	prior := []agent.ChatEvent{skillUse("unattended"), skillUse("admit")}
	out := buildSkillMatesOutput(skillCompleted("admit"), nightlyPair(), prior)
	assert.Nil(t, out.HookSpecificOutput)
}

// (c) A skill in no link group: nothing, however many linked skills sit
// beside it.
func TestBuildSkillMatesOutput_NonMemberSkill_Silent(t *testing.T) {
	out := buildSkillMatesOutput(skillCompleted("free"), nightlyPair(), nil)
	assert.Nil(t, out.HookSpecificOutput)
}

// (d) The completed tool is not Skill: nothing, even when its input happens to
// carry a "skill" key.
//
// MUTATION -- drop the ToolName == SkillToolName check in InvokedSkill --
// turns this red.
func TestBuildSkillMatesOutput_NonSkillTool_Silent(t *testing.T) {
	payload := claude.PostToolUsePayload{ToolName: "Bash", ToolInput: json.RawMessage(`{"skill":"admit","command":"ls"}`)}
	out := buildSkillMatesOutput(payload, nightlyPair(), nil)
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
	out := buildSkillMatesOutput(skillCompleted("closeout"), delivered, nil)
	assert.Equal(t, claude.SkillMatesContext("closeout", []string{"check-triggers", "prompt-human"}), contextOf(out))

	out = buildSkillMatesOutput(skillCompleted("closeout"), delivered, []agent.ChatEvent{skillUse("prompt-human")})
	assert.Equal(t, claude.SkillMatesContext("closeout", []string{"check-triggers"}), contextOf(out))
}
