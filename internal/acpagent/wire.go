package acpagent

import (
	"github.com/joshgarnett/agent-client-protocol-go/acp/api"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file carries the HAND-ROLLED ACP wire shapes the pinned SDK
// (joshgarnett@2025-09-02) predates: the initialize response's agentInfo, and
// the whole session-modes surface (session/new + session/load `modes` state,
// session/set_mode, the current_mode_update session update). The shapes mirror
// the current ACP schema; when the SDK gains them, these collapse onto its
// types.

// initializeResult is the initialize response body: the SDK's
// InitializeResponse plus the agentInfo identity block it predates (the
// mirror of the client driver's clientInfo).
type initializeResult struct {
	ProtocolVersion   int                   `json:"protocolVersion"`
	AgentCapabilities api.AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         agentInfoBlock        `json:"agentInfo"`
}

type agentInfoBlock struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DefaultModeID is frontend-neutral (ISO0): it lives in internal/operations
// (engine_types.go) alongside SessionModes, which uses it as the synthetic
// mode ID for the configured default profile set (which may compose several
// profiles, so no single profile name can stand for it).
const DefaultModeID = operations.DefaultModeID

// newSessionResult is the session/new response body: the SDK's
// NewSessionResponse plus the modes/models state it predates.
type newSessionResult struct {
	SessionId api.SessionId `json:"sessionId"`
	Modes     *modeState    `json:"modes,omitempty"`
	Models    *modelState   `json:"models,omitempty"`
}

// loadSessionResult is the session/load response body (modes + models state).
type loadSessionResult struct {
	Modes  *modeState  `json:"modes,omitempty"`
	Models *modelState `json:"models,omitempty"`
}

// modeState is the ACP session-mode state block.
type modeState struct {
	CurrentModeId  string     `json:"currentModeId"`
	AvailableModes []modeWire `json:"availableModes"`
}

// modeWire is one selectable session mode.
type modeWire struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

// setModeParams is the session/set_mode request body.
type setModeParams struct {
	SessionId api.SessionId `json:"sessionId"`
	ModeId    string        `json:"modeId"`
}

// modelState advertises the LLMs a session could run, mirroring the emerging
// ACP model-selection shape (availableModels + currentModelId). It is
// ADVERTISEMENT ONLY: the pinned SDK has no model surface, and ctxloom pins the
// engine's LLM at launch (a live mid-session switch is not implemented — there
// is no session/set_model here). A client can display the available engines and
// the launched one; selecting a different one is not yet honored.
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

// usageUpdate is the session/update variant reporting context-window usage and
// cumulative cost — the ACP session-usage RFD shape (`used`/`size`/`cost`). The
// pinned SDK predates the usage_update variant, so it is hand-rolled here (it
// collapses onto the SDK's type when the SDK gains it).
type usageUpdate struct {
	SessionUpdate string     `json:"sessionUpdate"` // always "usage_update"
	Used          int        `json:"used"`          // tokens currently in context
	Size          int        `json:"size"`          // total context-window size
	Cost          *usageCost `json:"cost,omitempty"`
}

// usageCost is the optional cumulative cost of a usage_update.
type usageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"` // ISO 4217
}

// sessionInfoUpdate is a session/update carrying one-time session metadata
// (model, permission mode, context window, MCP server status) so a client can
// render a model/session header. NOTE: session_info_update is NOT a variant in
// the pinned SDK, nor a settled ACP variant — this is a best-effort
// forward-looking shape; a client that doesn't recognize it simply ignores it.
type sessionInfoUpdate struct {
	SessionUpdate  string          `json:"sessionUpdate"` // always "session_info_update"
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	ContextWindow  int             `json:"contextWindow,omitempty"`
	McpServers     []mcpStatusWire `json:"mcpServers,omitempty"`
}

// mcpStatusWire is one MCP server's connection status in a session_info_update.
type mcpStatusWire struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// modeStateWire renders the session's mode state for the wire (nil when the
// session advertises no modes).
func modeStateWire(m *SessionModes) *modeState {
	if m == nil || len(m.Available) == 0 {
		return nil
	}
	out := &modeState{CurrentModeId: m.Current}
	for _, mode := range m.Available {
		out.AvailableModes = append(out.AvailableModes, modeWire{Id: mode.ID, Name: mode.Name})
	}
	return out
}

// currentModeUpdateWire is the session/update variant announcing a mode change.
func currentModeUpdateWire(modeID string) any {
	return struct {
		SessionUpdate string `json:"sessionUpdate"`
		CurrentModeId string `json:"currentModeId"`
	}{SessionUpdate: "current_mode_update", CurrentModeId: modeID}
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
