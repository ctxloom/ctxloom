package claude

import (
	"encoding/json"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// A link group's skills deliver together, but the model invokes them one at a
// time -- and the measured misses are all CONSEQUENT pairs: the second skill's
// condition is false when the human speaks and turns true only once the first
// skill has run, which the listing text cannot say. This file is the
// owner-session binding of the one ctxloom-owned step that closes that gap: at
// a skill's completion, name its link-group mates the session has not invoked
// yet. The membership comes from the group (bundles.UninvokedSkillMates); the
// moment comes from the engine's PostToolUse event; "not yet invoked" comes
// from the engine's own transcript, so no state is persisted anywhere.

// SkillToolName is the tool claude-code runs a skill through. Its input carries
// the invoked skill's name under `skill` (skillToolInput).
const SkillToolName = "Skill"

// skillToolInput is the Skill tool's input as claude-code sends it, reduced to
// the one field this binding reads.
type skillToolInput struct {
	Skill string `json:"skill"`
}

// InvokedSkill reports the skill one tool call invoked, and whether the call
// was a skill invocation at all. Only the Skill tool counts: another tool whose
// input happens to carry a `skill` key is not one. An undecodable or empty
// input is no invocation.
func InvokedSkill(toolName string, input json.RawMessage) (string, bool) {
	if toolName != SkillToolName {
		return "", false
	}
	var in skillToolInput
	if err := json.Unmarshal(input, &in); err != nil || in.Skill == "" {
		return "", false
	}
	return in.Skill, true
}

// SkillsInvoked derives "already invoked this session" from the transcript's
// own Skill tool_use records, so nothing has to be persisted to answer it.
//
// Main thread only. A subagent's invocation is written to the same transcript
// as a sidechain entry, but the owner session never saw that skill's body --
// so for the owner it is exactly as uninvoked as if it had never fired, and
// counting it would silence the line on the very miss this binding closes.
func SkillsInvoked(evs []agent.ChatEvent) func(string) bool {
	seen := make(map[string]bool)
	for _, ev := range evs {
		e := ev.Entry
		if e == nil || e.Type != agent.EntryTypeToolUse || e.Sidechain {
			continue
		}
		if name, ok := InvokedSkill(e.ToolName, e.ToolInput); ok {
			seen[name] = true
		}
	}
	return func(name string) bool { return seen[name] }
}

// SkillMatesContext renders the one line the hook injects: the completed skill
// and the mates it leaves uninvoked. Names only -- prompt wording is the
// human's voice. Empty when there are no mates, so the hook stays silent
// rather than attaching an empty statement.
func SkillMatesContext(completed string, mates []string) string {
	if len(mates) == 0 {
		return ""
	}
	return "Skill " + completed + " completed; linked skills not yet invoked this session: " + strings.Join(mates, ", ")
}

// BuildSkillMatesOutput decides what one PostToolUse payload earns: the
// skill-mates line when the completed tool was a Skill call whose link group
// has mates the session has not invoked yet, and silence otherwise -- a
// non-Skill tool, a skill in no group, or a group fully invoked.
//
// delivered is the skill set this run materialized for the engine (the same
// set the listing came from), and prior is the session transcript up to now.
func BuildSkillMatesOutput(payload PostToolUsePayload, delivered []*bundles.LoadedSkill, prior []agent.ChatEvent) PostToolUseOutput {
	completed, ok := InvokedSkill(payload.ToolName, payload.ToolInput)
	if !ok {
		return PostToolUseOutput{}
	}
	line := SkillMatesContext(completed, bundles.UninvokedSkillMates(delivered, completed, SkillsInvoked(prior)))
	if line == "" {
		return PostToolUseOutput{}
	}
	return PostToolUseOutput{HookSpecificOutput: &PostToolUseSpecificOutput{
		HookEventName:     HookEventPostToolUse,
		AdditionalContext: line,
	}}
}
