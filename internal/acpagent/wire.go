package acpagent

import (
	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file carries the ACP wire shapes that are NOT plain pass-throughs of
// the coder/acp-go-sdk (fork) wire types: the modes/models state riding session/new and
// session/load's response, and ctxloom's own (renamed, non-colliding)
// session-info extension. The initialize response's agentInfo and the
// session-modes surface (session/set_mode request/response,
// current_mode_update) used to be hand-rolled here too, ahead of the pinned
// SDK — SDK1 (2026-07-16) collapsed those onto the SDK's
// InitializeResponse/Implementation/SetSessionModeRequest/SessionModeState/
// SessionMode/CurrentModeUpdate, which the current spec defines identically
// to what H1 confirmed ctxloom already emitted.

// DefaultModeID is frontend-neutral (ISO0): it lives in internal/operations
// (engine_types.go) alongside SessionModes, which uses it as the synthetic
// mode ID for the configured default profile set (which may compose several
// profiles, so no single profile name can stand for it).
const DefaultModeID = operations.DefaultModeID

// newSessionResult is the session/new response body: api.NewSessionResponse's
// sessionId plus the modes/models state riding alongside it.
type newSessionResult struct {
	SessionId api.SessionId         `json:"sessionId"`
	Modes     *api.SessionModeState `json:"modes,omitempty"`
	Models    *modelState           `json:"models,omitempty"`
}

// loadSessionResult is the session/load response body (modes + models state).
type loadSessionResult struct {
	Modes  *api.SessionModeState `json:"modes,omitempty"`
	Models *modelState           `json:"models,omitempty"`
}

// modelState advertises the LLMs a session could run, mirroring the emerging
// ACP model-selection shape (availableModels + currentModelId).
//
// L0 checklist B4: `models`/`currentModelId`/`availableModels` are NOT a
// construct in the current spec (schema-v1.19.0) at all — model
// advertisement+selection there rides the generic SessionConfigOption /
// session/set_config_option mechanism (a `category: "model"` config option),
// which this re-vendor deliberately does NOT implement (that is slice CO1;
// see the SDK's SessionConfigOption type). DECISION:
// KEEP this hand-rolled advertisement-only shape for now rather than migrate
// it to SessionConfigOption in this slice — migrating is real behavior change
// (a client would need to read a different response shape and call a
// different request method), which is explicitly out of SDK1's scope; CO1
// does the migration. This shape is ADVERTISEMENT ONLY: the pinned SDK never
// had a model surface either, and ctxloom pins the engine's LLM at launch (a
// live mid-session switch is not implemented — there is no session/set_model
// call here, and — separately verified during this slice — the current spec
// has no session/set_model method or MethodSessionSetModel constant to call
// even if we wanted to; see the SDK1 report's model-delivery finding). A
// client can display the available engines and the launched one; selecting a
// different one is not yet honored.
type modelState struct {
	CurrentModelId  string      `json:"currentModelId,omitempty"`
	AvailableModels []modelWire `json:"availableModels"`
}

// modelWire is one advertised LLM: its ctxloom config label (id) and display name.
type modelWire struct {
	ModelId string `json:"modelId"`
	Name    string `json:"name"`
}

// modelStateWire renders a session's advertised LLMs for the wire (nil when the
// session advertises none).
func modelStateWire(l *SessionLLMs) *modelState {
	if l == nil || len(l.Available) == 0 {
		return nil
	}
	out := &modelState{CurrentModelId: l.Current}
	for _, m := range l.Available {
		out.AvailableModels = append(out.AvailableModels, modelWire{ModelId: m.ID, Name: m.Name})
	}
	return out
}

// ctxloomSessionInfoUpdate is ctxloom's own session/update carrying one-time
// session metadata (model, permission mode, context window, MCP server
// status) so a client can render a model/session header.
//
// L0 checklist B3 (the headline finding): this used to be emitted under the
// wire name "session_info_update". The CURRENT spec (schema-v1.19.0)
// independently stabilized session_info_update as something COMPLETELY
// DIFFERENT — session METADATA (title/updatedAt), not a model/context/mcp
// header (see this file's SessionUpdate-variant notes below). Both shapes are
// schema-valid objects, so no validator catches the collision — but a
// spec-conforming peer would silently DROP this frame's real content trying
// to read it as a title update, and ctxloom's own client half
// (internal/acp/mapping.go) would misread a real peer's session_info_update
// as an (empty) ctxloom header. DECISION: rename ctxloom's own extension off
// the colliding name rather than keep the collision — this shape is used
// ctxloom↔ctxloom only today (no other ACP agent emits it), so renaming
// breaks no one; re-init/reconnect is the only "migration" a client needs.
// See internal/acp/mapping.go's sessionInfoVariant for the client-side twin.
type ctxloomSessionInfoUpdate struct {
	SessionUpdate  string          `json:"sessionUpdate"` // always "ctxloom_session_info" — NOT the spec's session_info_update
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	ContextWindow  int             `json:"contextWindow,omitempty"`
	McpServers     []mcpStatusWire `json:"mcpServers,omitempty"`
}

// mcpStatusWire is one MCP server's connection status in a
// ctxloom_session_info update.
type mcpStatusWire struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// modeStateWire renders the session's mode state for the wire (nil when the
// session advertises no modes).
func modeStateWire(m *SessionModes) *api.SessionModeState {
	if m == nil || len(m.Available) == 0 {
		return nil
	}
	out := &api.SessionModeState{CurrentModeId: api.SessionModeId(m.Current)}
	for _, mode := range m.Available {
		out.AvailableModes = append(out.AvailableModes, api.SessionMode{Id: api.SessionModeId(mode.ID), Name: mode.Name})
	}
	return out
}

// currentModeUpdateWire is the session/update variant announcing a mode
// change (schema $defs/CurrentModeUpdate) — H1 confirmed this hand-rolled
// shape already matched the current spec exactly; it is now built through
// the real api.SessionUpdate union instead of an ad hoc anonymous struct.
func currentModeUpdateWire(modeID string) any {
	return api.SessionUpdate{
		CurrentModeUpdate: &api.SessionCurrentModeUpdate{CurrentModeId: api.SessionModeId(modeID)},
	}
}

// modeByID returns the session mode named by id.
func modeByID(m *SessionModes, id string) (SessionMode, bool) {
	for _, mode := range m.Available {
		if mode.ID == id {
			return mode, true
		}
	}
	return SessionMode{}, false
}

// stopReasonToACP maps an engine's completion stop reason onto the ACP
// vocabulary; anything unrecognized reads as a normal end of turn.
func stopReasonToACP(stop string) api.StopReason {
	switch stop {
	case string(api.StopReasonCancelled):
		return api.StopReasonCancelled
	case string(api.StopReasonMaxTokens):
		return api.StopReasonMaxTokens
	case string(api.StopReasonMaxTurnRequests):
		return api.StopReasonMaxTurnRequests
	case string(api.StopReasonRefusal):
		return api.StopReasonRefusal
	default:
		return api.StopReasonEndTurn
	}
}
