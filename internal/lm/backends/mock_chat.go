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
				if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "mock chat: " + msg.Text}}) || !complete("end_turn") {
					return ctx.Err()
				}
			}
		}
	}
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
