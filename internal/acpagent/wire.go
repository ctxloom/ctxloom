package acpagent

import (
	"github.com/joshgarnett/agent-client-protocol-go/acp/api"
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

// DefaultModeID is the synthetic mode representing the configured default
// profile set (which may compose several profiles, so no single profile name
// can stand for it).
const DefaultModeID = "default"

// newSessionResult is the session/new response body: the SDK's
// NewSessionResponse plus the modes state it predates.
type newSessionResult struct {
	SessionId api.SessionId `json:"sessionId"`
	Modes     *modeState    `json:"modes,omitempty"`
}

// loadSessionResult is the session/load response body (modes state only).
type loadSessionResult struct {
	Modes *modeState `json:"modes,omitempty"`
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

// modeAvailable reports whether id names one of the session's modes.
func modeAvailable(m *SessionModes, id string) bool {
	for _, mode := range m.Available {
		if mode.ID == id {
			return true
		}
	}
	return false
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
