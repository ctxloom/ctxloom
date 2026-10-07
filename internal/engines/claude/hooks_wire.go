package claude

import "encoding/json"

// This file is the single source of truth for Claude Code's PreToolUse hook
// wire protocol: the JSON Claude Code writes to a hook's stdin and the
// decision JSON it accepts on stdout. Consumers that sit on the hook wire
// (ltk's claude-code engine) import these types instead of redefining them.
// ctxloom's own hook verbs never do: they reach claude's wire through the
// hook codec (hookcodec.go, Engine.Hooks()).
//
// Contract (verified):
//   - Allow / pass-through: emit nothing, exit 0. The normal permission flow
//     proceeds (this does NOT auto-approve).
//   - Deny: stdout {"hookSpecificOutput":{"hookEventName":"PreToolUse",
//     "permissionDecision":"deny","permissionDecisionReason":"…"}}, exit 0.
//     The reason is fed back to the model.
//   - stderr (verified against code.claude.com/docs/en/hooks): on exit 0 —
//     which is every case above, deny included — stderr goes only to Claude
//     Code's own debug log. It reaches neither the model nor the user's
//     terminal without --debug. (Exit 2 feeds stderr to the model as the
//     blocking reason; any other nonzero exit shows the user stderr's first
//     line; this hook never uses either.) A hook that wants a human-visible
//     warning cannot rely on stderr — see App.Warn's doc in ltk's app package
//     for the consequence.

// hookEventPreToolUse is the event name carried in payloads and decisions.
const hookEventPreToolUse = "PreToolUse"

// HookEventUserPromptSubmit is claude's native event for the unified
// turn_start: it fires when a prompt is submitted, before the model runs, and
// a command hook's stdout becomes context of that turn. ctxloom's own
// turn-start hook (`ctxloom hook mail-drain`) emits UserPromptSubmitOutput.
const HookEventUserPromptSubmit = "UserPromptSubmit"

// permissionDeny is the permissionDecision value that blocks the tool call.
const permissionDeny = "deny"

// HookPayload is the JSON Claude Code writes to a PreToolUse hook's stdin.
type HookPayload struct {
	SessionID      string    `json:"session_id,omitempty"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	HookEventName  string    `json:"hook_event_name,omitempty"`
	ToolName       string    `json:"tool_name"`
	ToolInput      ToolInput `json:"tool_input"`
}

// ToolInput carries the union of tool-input fields ltk-style consumers need:
// the command for Bash/PowerShell and the target for the file-editing tools —
// file_path for Edit/Write/MultiEdit, notebook_path for NotebookEdit (which
// does not send file_path).
type ToolInput struct {
	Command      string `json:"command,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	NotebookPath string `json:"notebook_path,omitempty"`
}

// HookOutput is the decision JSON a hook writes to stdout to deny a tool
// call. Allowing requires no output at all.
type HookOutput struct {
	HookSpecificOutput HookSpecificOutput `json:"hookSpecificOutput"`
}

// HookSpecificOutput is the PreToolUse permission decision.
type HookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// --- the context envelope ---------------------------------------------------

// HookEventSessionStart is claude's native event for the unified
// session_start.
const HookEventSessionStart = "SessionStart"

// hookEventPostToolUse is claude's native event for the unified post_tool
// (and, narrowed by a matcher, post_file_edit).
const hookEventPostToolUse = "PostToolUse"

// AdditionalContextOutput carries the additional context to inject. It is the
// hookSpecificOutput of every event whose stdout becomes model-visible
// context — SessionStart, UserPromptSubmit and PostToolUse — with
// HookEventName naming which; Claude Code refuses an envelope whose event does
// not match the hook that produced it. The hook codec (hookcodec.go) is its
// one writer.
type AdditionalContextOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// DecodeHookPayload parses a hook stdin payload.
func DecodeHookPayload(data []byte) (HookPayload, error) {
	var p HookPayload
	err := json.Unmarshal(data, &p)
	return p, err
}

// EncodeDeny renders the deny decision Claude Code expects on stdout.
func EncodeDeny(reason string) ([]byte, error) {
	return json.Marshal(HookOutput{HookSpecificOutput: HookSpecificOutput{
		HookEventName:            hookEventPreToolUse,
		PermissionDecision:       permissionDeny,
		PermissionDecisionReason: reason,
	}})
}
