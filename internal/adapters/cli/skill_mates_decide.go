package cli

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// buildSkillMatesOutput decides what one PostToolUse payload earns: the
// skill-mates line when the completed tool was a Skill call whose link group
// has mates the session has not invoked yet, and silence otherwise -- a
// non-Skill tool, a skill in no group, or a group fully invoked.
//
// delivered is the skill set this run materialized for the engine (the same
// set the listing came from), and prior is the session transcript up to now.
//
// It lives here, not in internal/engines/claude, because it is typed on the bundle
// model and the lean binaries (ltk, taskloom) link internal/engines/claude for its
// wire types but must never link internal/core/bundles (tests/arch).
func buildSkillMatesOutput(payload claude.PostToolUsePayload, delivered []*bundles.LoadedSkill, prior []agent.ChatEvent) claude.PostToolUseOutput {
	completed, ok := claude.InvokedSkill(payload.ToolName, payload.ToolInput)
	if !ok {
		return claude.PostToolUseOutput{}
	}
	line := claude.SkillMatesContext(completed, bundles.UninvokedSkillMates(delivered, completed, claude.SkillsInvoked(prior)))
	if line == "" {
		return claude.PostToolUseOutput{}
	}
	return claude.PostToolUseOutput{HookSpecificOutput: &claude.PostToolUseSpecificOutput{
		HookEventName:     claude.HookEventPostToolUse,
		AdditionalContext: line,
	}}
}
