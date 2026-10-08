package claude

import "encoding/json"

// The skill half of claude's hook wire: claude runs every skill through one
// tool, whose input names the skill. The hook codec answers "which skill did
// this call run" from it (hookCodec.InvokedSkill), for a PostToolUse payload
// and for a transcript's tool_use record alike; the skill-mates decision that
// uses the answer is engine-neutral and lives with the hook verb.

// skillToolName is the tool claude-code runs a skill through. Its input
// carries the invoked skill's name under `skill` (skillToolInput).
const skillToolName = "Skill"

// skillToolInput is the Skill tool's input as claude-code sends it, reduced to
// the one field read here.
type skillToolInput struct {
	Skill string `json:"skill"`
}

// invokedSkill reports the skill one tool call invoked, and whether the call
// was a skill invocation at all. Only the Skill tool counts: another tool whose
// input happens to carry a `skill` key is not one. An undecodable or empty
// input is no invocation.
func invokedSkill(toolName string, input []byte) (string, bool) {
	if toolName != skillToolName {
		return "", false
	}
	var in skillToolInput
	if err := json.Unmarshal(input, &in); err != nil || in.Skill == "" {
		return "", false
	}
	return in.Skill, true
}
