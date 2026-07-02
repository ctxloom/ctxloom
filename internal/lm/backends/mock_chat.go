package backends

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Mock offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Mock)(nil)

// Chat implements a deterministic ECHO conversation for the mock backend: each
// inbound message yields one assistant entry ("mock chat: <text>") and a turn
// completion. It exists so structured-chat plumbing — the gRPC Chat bridge and
// the ACP agent server — can be conformance-tested hermetically, with the
// echoed text proving exactly what was delivered to the engine (context lead
// blocks included).
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "mock chat: " + msg.Text}}) {
				return ctx.Err()
			}
			if !send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", Model: req.Model}}) {
				return ctx.Err()
			}
		}
	}
}
