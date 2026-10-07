package claude

import (
	"encoding/json"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// hookCodec is claude's hook wire in the port's terms (engine.HookCodec):
// the JSON claude writes to a hook's stdin, decoded, and the JSON it reads
// back from stdout, encoded. It is the only code that knows either shape;
// ctxloom's hook verbs reach it through Engine.Hooks().
type hookCodec struct{}

// hookWire is the union of the stdin fields claude writes across the events
// ctxloom hooks: every payload carries the session and transcript; the rest
// is per event. tool_input and tool_response stay raw — their shape is per
// tool, and a codec that modelled them would fail on every tool it had not.
type hookWire struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	HookEventName  string          `json:"hook_event_name"`
	Source         string          `json:"source"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

// Decode reads one payload. The event is the payload's own hook_event_name
// when present, else the event the hook was registered under; either is
// mapped to the unified name (a narrowed event — pre_shell, post_file_edit —
// decodes as the broad one claude fired, since claude's payload names that).
func (hookCodec) Decode(event string, payload []byte) (engine.HookEvent, error) {
	var p hookWire
	if err := json.Unmarshal(payload, &p); err != nil {
		return engine.HookEvent{}, fmt.Errorf("%s hook payload: %w", EngineName, err)
	}
	native := p.HookEventName
	if native == "" {
		native = event
	}
	out := engine.HookEvent{
		Event:         unifiedHookEvent(native),
		NativeSession: p.SessionID,
		Transcript:    p.TranscriptPath,
		Source:        p.Source,
		Prompt:        p.Prompt,
		Tool:          p.ToolName,
		ToolInput:     p.ToolInput,
		ToolResponse:  p.ToolResponse,
		Path:          editedPath(p.ToolInput),
	}
	out.Skill, _ = invokedSkill(p.ToolName, p.ToolInput)
	return out, nil
}

// unifiedHookEvent maps a native or unified event name to the unified one;
// a name claude does not fire is carried verbatim.
func unifiedHookEvent(name string) string {
	for _, e := range nativeHookEvents {
		if e.native == name || e.unified == name {
			return e.unified
		}
	}
	return name
}

// editedPath is the file a file-editing tool targeted: file_path for
// Edit/Write/MultiEdit, notebook_path for NotebookEdit (which sends no
// file_path). Any other input names none.
func editedPath(input json.RawMessage) string {
	var in ToolInput
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.NotebookPath
}

// hookEnvelope is the stdout envelope claude reads from a hook: model context
// rides hookSpecificOutput (whose hookEventName must name the native event
// that fired), the user notice rides systemMessage, and a block rides
// decision/reason.
type hookEnvelope struct {
	Decision           string                   `json:"decision,omitempty"`
	Reason             string                   `json:"reason,omitempty"`
	HookSpecificOutput *AdditionalContextOutput `json:"hookSpecificOutput,omitempty"`
	SystemMessage      string                   `json:"systemMessage,omitempty"`
}

// decisionBlock is the decision value that stops what the event was about to
// do (the prompt, on UserPromptSubmit).
const decisionBlock = "block"

// contextEvents are the native events whose hookSpecificOutput carries
// additionalContext to the model, by the unified events registered on them.
var contextEvents = map[string]string{
	wire.HookEventSessionStart: HookEventSessionStart,
	wire.HookEventTurnStart:    HookEventUserPromptSubmit,
	wire.HookEventPostTool:     hookEventPostToolUse,
	wire.HookEventPostFileEdit: hookEventPostToolUse,
}

// blockEvents are the native events claude lets a hook block with a
// decision, by the unified events registered on them.
var blockEvents = map[string]bool{
	wire.HookEventTurnStart:    true,
	wire.HookEventPostTool:     true,
	wire.HookEventPostFileEdit: true,
	wire.HookEventTurnEnd:      true,
}

// Encode renders r as claude's answer to a hook registered under event. Every
// answer exits 0: claude reads a non-zero exit as the hook failing, which no
// answer here means.
func (hookCodec) Encode(event string, r engine.HookResponse) (engine.HookReply, error) {
	var a hookEnvelope
	if r.Context != "" {
		native, ok := contextEvents[event]
		if !ok {
			return engine.HookReply{}, fmt.Errorf("%s carries no hook context on %s", EngineName, event)
		}
		a.HookSpecificOutput = &AdditionalContextOutput{HookEventName: native, AdditionalContext: r.Context}
	}
	if r.Block {
		if !blockEvents[event] {
			return engine.HookReply{}, fmt.Errorf("%s cannot block %s from a hook", EngineName, event)
		}
		a.Decision, a.Reason = decisionBlock, r.Reason
	}
	a.SystemMessage = r.Notice
	b, err := json.Marshal(a)
	if err != nil {
		return engine.HookReply{}, err
	}
	return engine.HookReply{Stdout: append(b, '\n')}, nil
}

// additionalContextMaxChars bounds one hook's additionalContext. Claude caps
// a single hook's additionalContext at ~10,000 chars: output above that is
// persisted to a file and only a ~2KB preview reaches the model. This keeps a
// safety margin under that cap.
const additionalContextMaxChars = 7500

// ContextLimit is claude's per-hook context cap.
func (hookCodec) ContextLimit() int { return additionalContextMaxChars }

// InvokedSkill is the skill a claude tool call ran (invokedSkill).
func (hookCodec) InvokedSkill(tool string, input []byte) (string, bool) {
	return invokedSkill(tool, input)
}

// toolMatchers is claude's native matcher for each neutral tool class: the
// tool names claude runs a shell command, a file edit and a skill through.
var toolMatchers = map[wire.ToolClass]string{
	wire.ToolShell:    "Bash",
	wire.ToolFileEdit: "Edit|Write",
	wire.ToolSkill:    skillToolName,
}

// toolMatcher maps a tool class to claude's matcher (agent.BindHooks).
func toolMatcher(c wire.ToolClass) (string, bool) {
	m, ok := toolMatchers[c]
	return m, ok
}
