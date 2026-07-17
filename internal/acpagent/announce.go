package acpagent

import (
	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// This file is ISO3/ISO4's at-connect delivery half: EngineChat.InitSummary
// (the SESSION INITIALIZATION SUMMARY text —
// internal/operations/engine_session.go's buildSessionInitSummary) crosses
// into a visible ACP frame here, mirroring commands.go's
// emitAvailableCommands almost exactly. Both exist for the same reason:
// session/new and session/load's response schemas have no field for "a
// message to show the user before any turn runs", so a session/update
// notification sent right after the session opens — but before the RPC
// reply — is what's available. See handleSessionNew/handleSessionLoad
// (server.go) for the call sites.
//
// Why this couldn't just stay on the engine's Events channel (the mechanism
// before this slice, operations.announceOnFirstEvent, now deleted): this
// server only ever drains sess.engine.Events from inside runTurn, which
// session/prompt starts — so an editor that connects and never sends a
// prompt (or is simply slow to send its first one) saw NOTHING. A
// session/update notification has no such gate: it is delivered the moment
// the session exists, exactly like available_commands_update.

// emitSessionInitSummary sends the session's ISO3/ISO4 initialization
// summary as an agent_message_chunk session/update notification. Called
// once right after the session opens (handleSessionNew/handleSessionLoad),
// before the session/new|load reply and before the connected editor can
// possibly have sent a session/prompt. A no-op when the session carries no
// summary text (sessionInitSummaryUpdateWire returns nil and emitUpdate
// treats nil as nothing to send) — in production this never happens
// (OpenEngineSession computes a summary for every posture, including the
// fully unisolated common case), but a test double's EngineChat may
// legitimately leave it unset.
func (s *Server) emitSessionInitSummary(sess *session) *jsonrpc.Error {
	return s.emitUpdate(sess, sessionInitSummaryUpdateWire(sess.engine.InitSummary))
}

// sessionInitSummaryUpdateWire renders the initialization summary as an ACP
// agent_message_chunk — the SAME wire shape ordinary assistant text takes,
// deliberately NOT `_meta`: a foreign client is free to ignore `_meta` by
// contract, and the summary (especially ISO3's agent-not-found WARNING, and
// ISO4's MCP status — which has no other spec-legal home) must be a message
// no conformant client can silently drop. nil when text is "".
func sessionInitSummaryUpdateWire(text string) any {
	if text == "" {
		return nil
	}
	return api.SessionUpdate{AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(text)}}
}
