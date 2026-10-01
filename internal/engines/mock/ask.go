package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A mock:ask turn makes one tool call its rules leave open and asks about it
// the way claude -p does in a run whose approver is the human: the call goes
// out on the stream, then the engine runs the delivered permission_ask hooks
// with the call as its codec reads it (no call id, as claude's
// PermissionRequest carries none); the first hook that answers decides, and
// with none answering the call is denied — nobody sits at the engine. Any
// other approver is nobody to ask: the call is denied at once, as
// --permission-prompts none denies it.

// askPattern is the marker: `mock:ask=<tool>:<input JSON, no spaces>`.
var askPattern = regexp.MustCompile(`mock:ask=([^:\s]+):(\S+)`)

// Ask renders the prompt directive that makes the mock's turn ask about a
// call to tool with input.
func Ask(tool, input string) string { return "mock:ask=" + tool + ":" + input }

// AskCallID is the id of the call a mock:ask turn makes.
const AskCallID = "mock-ask-1"

// errAskInput refuses a marker whose input is not JSON.
var errAskInput = errors.New("mock: the mock:ask input is not JSON")

// askIn reads the call a prompt asks the turn to make.
func askIn(prompt string) (string, json.RawMessage, bool, error) {
	m := askPattern.FindStringSubmatch(prompt)
	if m == nil {
		return "", nil, false, nil
	}
	if !json.Valid([]byte(m[2])) {
		return "", nil, true, fmt.Errorf("%w: %s", errAskInput, m[2])
	}
	return m[1], json.RawMessage(m[2]), true, nil
}

// askDecision is the mock codec's answer.
type askDecision struct {
	Allow   bool   `json:"allow"`
	Message string `json:"message"`
}

// ask makes the call and has it decided; a denied call is returned as the
// denial the turn reports.
func (d driver) ask(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, send func(agent.ChatEvent) error, tool string, input json.RawMessage) (*agent.PermissionDenial, error) {
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: tool, ToolCallID: AskCallID, ToolInput: input}}); err != nil {
		return nil, err
	}
	decision := askDecision{Message: "mock: nobody is asked; " + tool + " is denied"}
	if d.approver == engine.ApproverHuman {
		var err error
		if decision, err = askTheHuman(ctx, ex, hooks, tool, input); err != nil {
			return nil, err
		}
	}
	result := agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: AskCallID, ToolOutput: "mock: " + tool + " ran"}
	if !decision.Allow {
		result.ToolOutput, result.IsError = decision.Message, true
	}
	if err := send(agent.ChatEvent{Entry: &result}); err != nil {
		return nil, err
	}
	if decision.Allow {
		return nil, nil
	}
	denial := agent.PermissionDenial{ToolName: tool, ToolCallID: AskCallID, Reason: decision.Message, Decider: agent.DeciderPolicy}
	return &denial, send(agent.ChatEvent{Denied: &denial})
}

// askTheHuman runs the permission_ask hooks; the first one that answers
// decides, and with none answering the call is denied.
func askTheHuman(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, tool string, input json.RawMessage) (askDecision, error) {
	payload, err := json.Marshal(mockCall{Tool: tool, Input: input})
	if err != nil {
		return askDecision{}, err
	}
	dec, answered, err := firstHookAnswer(ctx, ex, hooks.PermissionAsk, tool, payload)
	if err != nil || answered {
		return dec, err
	}
	return askDecision{Message: "mock: no hook decided; " + tool + " is denied"}, nil
}

// firstHookAnswer runs the hooks that admit tool, in order, and returns the
// first decision one of them writes; a hook that fails or writes nothing
// decides nothing.
func firstHookAnswer(ctx context.Context, ex engine.Exec, hooks []wire.Hook, tool string, payload []byte) (askDecision, bool, error) {
	for _, h := range hooks {
		ok, err := hookMatches(h, tool)
		if err != nil {
			return askDecision{}, false, err
		}
		if !ok {
			continue
		}
		out, err := runHook(ctx, h.Command, payload, ex.WorkDir, ex.Env)
		var dec askDecision
		if err == nil && len(out) > 0 && json.Unmarshal(out, &dec) == nil {
			return dec, true, nil
		}
	}
	return askDecision{}, false, nil
}

// hookMatches reports whether h's matcher admits tool (no matcher admits
// every tool).
func hookMatches(h wire.Hook, tool string) (bool, error) {
	if h.Matcher == "" {
		return true, nil
	}
	ok, err := regexp.MatchString("^(?:"+h.Matcher+")$", tool)
	if err != nil {
		return false, fmt.Errorf("mock: hook matcher %q: %w", h.Matcher, err)
	}
	return ok, nil
}
