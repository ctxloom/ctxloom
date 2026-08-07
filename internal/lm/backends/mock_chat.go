package backends

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Mock offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Mock)(nil)

// Chat implements a deterministic conversation for the mock backend, so
// structured-chat plumbing — the gRPC Chat bridge and the ACP agent server —
// can be conformance-tested hermetically. The default turn is an ECHO: one
// assistant entry ("mock chat: <text>") plus a completion, with the echoed text
// proving exactly what was delivered to the engine (context lead blocks
// included). Two message markers script the control paths:
//
//   - "PERMISSION" (with ForwardPermissions): the turn emits a permission
//     request (options allow/reject) and parks until the answer arrives, then
//     echoes the verdict ("mock chat: permission granted|denied|dismissed").
//   - "HANG": the turn parks without completing until a CancelTurn message
//     arrives, then completes with StopReason "cancelled" — the per-turn cancel
//     seam, exercised without a real engine.
//   - "TOOLS": the turn emits the FULL entry vocabulary a real engine produces
//     — thinking, tool_use, tool_result, assistant — before completing. Added
//     for the container-progress assertion (light-foe): a liveness check that
//     asserts entry-type VARIETY is only meaningful against a stub that can
//     actually produce more than one type, and the echo default cannot. Every
//     entry carries the turn's text so the payload still proves exactly what
//     was delivered to the engine.
func (b *Mock) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	complete := func(stop string) bool {
		return send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: stop, Model: req.Model}})
	}
	permSeq := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Permission != nil || msg.CancelTurn {
				continue // stray control message between turns
			}
			switch {
			case req.ForwardPermissions && strings.Contains(msg.Text, "PERMISSION"):
				permSeq++
				verdict, ok := b.chatPermissionTurn(ctx, permSeq, in, send)
				if !ok {
					return ctx.Err()
				}
				if verdict == "" {
					return nil // input closed mid-park
				}
				if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "mock chat: permission " + verdict}}) || !complete("end_turn") {
					return ctx.Err()
				}
			case strings.Contains(msg.Text, "TOOLS"):
				if !b.chatToolsTurn(msg.Text, send) || !complete("end_turn") {
					return ctx.Err()
				}
			case strings.Contains(msg.Text, "HANG"):
				stop, ok := b.chatHangTurn(ctx, in)
				if !ok {
					return ctx.Err()
				}
				if stop == "" {
					return nil // input closed mid-park
				}
				if !complete(stop) {
					return ctx.Err()
				}
			default:
				if err := b.recordChatTurn(req, msg.Text); err != nil {
					return err
				}
				if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "mock chat: " + msg.Text}}) || !complete("end_turn") {
					return ctx.Err()
				}
			}
		}
	}
}

// recordChatTurn writes this turn's record when CTXLOOM_MOCK_RECORD_FILE is
// set, the same evidence Execute leaves.
//
// Chat and Execute are not interchangeable arms of one path — they are the ONLY
// arms, split by transport: a container structured/oneshot run goes through
// Chat (run.go's armOwnedRunContainer), everything else through Execute. While
// only Execute recorded, the container arm left no evidence at all, so a run
// that never containerized and a run that containerized perfectly produced the
// identical observable: exit 0 and an echo. Every isolation scenario reads this
// record to decide WHERE the engine ran, and none of them could be written for
// the one axis where the question matters most.
//
// A write failure is returned rather than warned, for the reason Execute's own
// path documents: reporting success with no record file lets a later assertion
// silently read a STALE record from a previous run.
//
// The turn text is recorded as the prompt because on this arm it IS the
// prompt — the host composes context and prompt into one lead block
// (JoinLeadBlocks) before the turn is issued.
func (b *Mock) recordChatTurn(req agent.ChatRequest, text string) error {
	return writeMockRecord(getEnvFromMap(req.Env, "CTXLOOM_MOCK_RECORD_FILE"), mockRecordFields{
		WorkDir:       req.WorkDir,
		Env:           req.Env,
		Prompt:        text,
		Context:       agent.AssembleContext(b.fragments),
		FragmentCount: len(b.fragments),
	}, b.managed)
}

// chatPermissionTurn emits one permission request and parks until its answer.
// Returns the verdict ("" = input closed) and whether ctx survived.
func (b *Mock) chatPermissionTurn(ctx context.Context, seq int, in <-chan agent.ChatMessage, send func(agent.ChatEvent) bool) (string, bool) {
	id := "mock-perm-" + strconv.Itoa(seq)
	req := &agent.PermissionRequest{
		ID:        id,
		ToolName:  "mock_tool",
		ToolInput: json.RawMessage(`{"action":"scripted"}`),
		Options: []agent.PermissionOption{
			{ID: "allow", Kind: "allow_once", Name: "Allow"},
			{ID: "reject", Kind: "reject_once", Name: "Reject"},
		},
	}
	if !send(agent.ChatEvent{Permission: req}) {
		return "", false
	}
	for {
		select {
		case <-ctx.Done():
			return "", false
		case msg, ok := <-in:
			if !ok {
				return "", true
			}
			if msg.Permission == nil || msg.Permission.ID != id {
				continue
			}
			switch msg.Permission.OptionID {
			case "allow":
				return "granted", true
			case "":
				return "dismissed", true
			default:
				return "denied", true
			}
		}
	}
}

// chatToolsTurn emits one thinking → tool_use → tool_result → assistant
// sequence for text, returning false when the consumer went away. The tool
// call is entirely synthetic (nothing is executed): the point is the ENTRY
// VOCABULARY on the wire and in the canonical transcript, not tool behavior.
func (b *Mock) chatToolsTurn(text string, send func(agent.ChatEvent) bool) bool {
	return send(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeThinking,
		Content: "mock thinking: " + text,
	}}) && send(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolUse,
		ToolName:   "mock_tool",
		ToolCallID: "mock-tool-1",
		ToolInput:  json.RawMessage(`{"action":"scripted"}`),
		Content:    "mock tool_use: " + text,
	}}) && send(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolName:   "mock_tool",
		ToolCallID: "mock-tool-1",
		ToolOutput: "mock tool_result: " + text,
		Content:    "mock tool_result: " + text,
	}}) && send(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeAssistant,
		Content: "mock chat: " + text,
	}})
}

// chatHangTurn parks until a CancelTurn arrives, completing the turn with
// "cancelled". Returns the stop reason ("" = input closed) and ctx survival.
func (b *Mock) chatHangTurn(ctx context.Context, in <-chan agent.ChatMessage) (string, bool) {
	for {
		select {
		case <-ctx.Done():
			return "", false
		case msg, ok := <-in:
			if !ok {
				return "", true
			}
			if msg.CancelTurn {
				return "cancelled", true
			}
		}
	}
}
