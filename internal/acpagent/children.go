package acpagent

import (
	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Tier A push (manly-grant item 2) — a delegated child's
// live activity surfaces in the connected editor's transcript as it happens,
// not only via agent_recv/roster polling. This file is deliberately
// decoupled from internal/agentcoord/coord (the same layering EngineChat
// itself uses for the engine substrate): WatchChildren below is an
// ABSTRACT subscription the CLI wires to the real coordinator's
// ConsumerService/WatchRuns; acpagent only ever sees the small ChildUpdate
// shape.

// ChildUpdate is frontend-neutral (ISO0): it lives in internal/operations
// (engine_types.go) alongside EngineChat, whose WatchChildren field carries
// it. An alias, not a new type — every existing acpagent.ChildUpdate
// reference keeps compiling unchanged.
//
// ChildUpdateKind used to be aliased here too, with zero
// references anywhere in the repo outside its own declaration (not even
// test-only) — deleted; use operations.ChildUpdateKind directly if a
// consumer ever needs it.
type ChildUpdate = operations.ChildUpdate

const (
	// ChildUpdateStarted marks a delegated child's run beginning.
	ChildUpdateStarted = operations.ChildUpdateStarted
	// ChildUpdateMessage carries a chunk of the child's own output.
	ChildUpdateMessage = operations.ChildUpdateMessage
	// ChildUpdateCompleted marks a delegated child's run ending.
	ChildUpdateCompleted = operations.ChildUpdateCompleted
)

// pushChildUpdates runs for the session's lifetime (its ctx is cancelled on
// session close): subscribes via engine.WatchChildren and forwards each
// update as a session/update agent_message_chunk notification, prefixed so
// the child's identity is visible in the transcript — the pinned SDK has no
// dedicated sub-agent update variant, so this reuses the same channel the
// session's OWN output rides (mapping.go's EntryTypeAssistant case). A nil
// WatchChildren (no coordinator hosted, or delegation degraded) means this
// never starts — the session behaves exactly as it did before this feature
// existed.
func (s *Server) pushChildUpdates(sess *session) {
	if sess.engine.WatchChildren == nil {
		return
	}
	updates, cancel := sess.engine.WatchChildren(sess.ctx)
	if updates == nil {
		return
	}
	defer cancel()
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return
			}
			if err := s.emitUpdate(sess, childUpdateWire(u)); err != nil {
				clidiag.Warn("ctxloom", "acp: push child update for %s: %v", sess.id, err.Message)
				return
			}
		case <-sess.ctx.Done():
			return
		}
	}
}

// childUpdateWire renders a ChildUpdate as an agent_message_chunk update,
// prefixed with the child's harp so a human reading the transcript can tell
// a delegated child's activity from the session's own engine output.
func childUpdateWire(u ChildUpdate) api.SessionUpdate {
	prefix := "[child " + u.Harp + "] "
	switch u.Kind {
	case ChildUpdateStarted:
		prefix += "started: "
	case ChildUpdateCompleted:
		prefix += "completed: "
	}
	return api.SessionUpdate{
		AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(prefix + u.Text)},
	}
}
