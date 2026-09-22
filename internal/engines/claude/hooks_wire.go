package claude

import "encoding/json"

// This file is the single source of truth for Claude Code's PreToolUse hook
// wire protocol: the JSON Claude Code writes to a hook's stdin and the
// decision JSON it accepts on stdout. Consumers that sit on the hook wire
// (ltk's claude-code engine) import these types instead of redefining them.
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

// --- SessionStart wire shapes ----------------------------------------------
//
// SessionStart is the context-injection event: Claude Code writes session
// identity to the hook's stdin and
// accepts an additionalContext envelope on stdout. ctxloom's hook targets
// (`ctxloom hook inject-context`, `ctxloom hook session-bind`) sit on this
// wire; they import these types instead of redefining them.

// HookEventSessionStart is the event name carried in SessionStart decisions.
const HookEventSessionStart = "SessionStart"

// SessionStartPayload is the JSON Claude Code writes to a SessionStart hook's
// stdin: the session id, the path of the engine's own transcript file (what
// the bind step records forward), and the launch kind.
type SessionStartPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"` // startup|resume|clear|compact
}

// SessionStartOutput is the JSON a SessionStart hook writes to stdout to
// inject context. An empty output (no hookSpecificOutput) injects nothing.
type SessionStartOutput struct {
	HookSpecificOutput *AdditionalContextOutput `json:"hookSpecificOutput,omitempty"`
	// SystemMessage rides a separate channel from HookSpecificOutput: Claude
	// Code surfaces it to the user in the terminal, NOT to the model. ctxloom
	// uses it to nudge the user toward /recover after a /clear, where the model
	// gets no recovered context but the human should know it can be pulled back.
	SystemMessage string `json:"systemMessage,omitempty"`
}

// AdditionalContextOutput carries the additional context to inject. It is the
// hookSpecificOutput of every event whose stdout becomes model-visible
// context — SessionStart and UserPromptSubmit — with HookEventName naming
// which; Claude Code refuses an envelope whose event does not match the hook
// that produced it.
type AdditionalContextOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// --- UserPromptSubmit wire shapes ------------------------------------------

// UserPromptSubmitOutput is the JSON a UserPromptSubmit hook writes to stdout
// to add context to the turn that is starting. An empty output injects
// nothing; a hook with nothing to say writes no envelope at all, because an
// envelope with an empty additionalContext is still an event the model sees.
type UserPromptSubmitOutput struct {
	HookSpecificOutput *AdditionalContextOutput `json:"hookSpecificOutput,omitempty"`
}

// --- PostToolUse wire shapes -----------------------------------------------

// HookEventPostToolUse is the event name a PostToolUse decision carries back.
const HookEventPostToolUse = "PostToolUse"

// PostToolUsePayload is the JSON written to a PostToolUse hook's stdin.
// ToolInput and ToolResponse are left RAW because their shape is per-tool (a
// string for some, an object for others): the reflect hook needs only the
// response's size, the skill-mates hook only the Skill tool's input
// (InvokedSkill), and decoding either into a concrete type would make a hook
// fail on every tool it had not modelled.
type PostToolUsePayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

// PostToolUseOutput is the JSON a PostToolUse hook writes to stdout. An empty
// output injects nothing, which is the common case: the hook stays silent
// below its threshold.
type PostToolUseOutput struct {
	HookSpecificOutput *PostToolUseSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// PostToolUseSpecificOutput carries context injected after a tool call.
type PostToolUseSpecificOutput struct {
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

// --- Stop wire shape -------------------------------------------------------

// StopPayload is the JSON Claude Code writes to a Stop hook's stdin. It fires
// when a turn is about to end; transcript_path points at the session's own
// native JSONL store, which is what makes the TURN (rather than the working
// tree) measurable from a hook.
//
// StopHookActive is set when the turn is already resuming because a Stop hook
// blocked. A hook that blocks again while it is true is an infinite loop, so
// every Stop hook must exit first and unconditionally on it.
type StopPayload struct {
	SessionID      string `json:"session_id,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	HookEventName  string `json:"hook_event_name,omitempty"`
	StopHookActive bool   `json:"stop_hook_active,omitempty"`
}

// DecodeStopPayload parses a Stop hook stdin payload.
func DecodeStopPayload(data []byte) (StopPayload, error) {
	var p StopPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
