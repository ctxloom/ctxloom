package acpagent

import (
	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file carries the ACP wire shapes that are NOT plain pass-throughs of
// the coder/acp-go-sdk (fork) wire types: the modes/models state riding session/new and
// session/load's response, and ctxloom's own (renamed, non-colliding)
// session-info extension — which IR4 additionally moved off its own bespoke
// top-level `sessionUpdate` variant onto the spec's `_meta` extension channel
// riding a REAL `session_info_update` frame (see ctxloomSessionInfoUpdate's
// doc comment for the full why). The initialize response's agentInfo and the
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
// sessionId plus the modes/models state riding alongside it, PLUS (CO1) the
// spec-general configOptions surface — see configOptionsWire. COMPAT: modes
// and models are NOT retired in favor of configOptions — they ride alongside
// it through a transitional window (see this file's package doc and
// claudeModelSelectionQuirk's sibling doc in internal/claude/chat.go): target
// clients (agentic.nvim, formulahendry's picker) drive session/set_mode and
// the models advertisement TODAY and almost certainly do not speak
// session/set_config_option yet.
type newSessionResult struct {
	SessionId     api.SessionId             `json:"sessionId"`
	Modes         *api.SessionModeState     `json:"modes,omitempty"`
	Models        *modelState               `json:"models,omitempty"`
	ConfigOptions []api.SessionConfigOption `json:"configOptions,omitempty"`
}

// loadSessionResult is the session/load response body (modes + models state,
// plus CO1's configOptions — see newSessionResult's doc comment on compat).
type loadSessionResult struct {
	Modes         *api.SessionModeState     `json:"modes,omitempty"`
	Models        *modelState               `json:"models,omitempty"`
	ConfigOptions []api.SessionConfigOption `json:"configOptions,omitempty"`
}

// modelState advertises the LLMs a session could run, mirroring the emerging
// ACP model-selection shape (availableModels + currentModelId).
//
// L0 checklist B4: `models`/`currentModelId`/`availableModels` are NOT a
// construct in the current spec (schema-v1.19.0) at all — model
// advertisement+selection there rides the generic SessionConfigOption /
// session/set_config_option mechanism (a `category: "model"` config option,
// see configOptionsWire's "model" entry). CO1 now ALSO emits that spec-general
// surface, but this hand-rolled shape is KEPT and still emitted ALONGSIDE it
// (see newSessionResult's compat doc comment) rather than retired — a
// pre-CO1 client keeps working unchanged. This shape stays ADVERTISEMENT
// ONLY (a live mid-session switch via THIS field was never implemented and
// still isn't — a client wanting to change models must go through
// session/set_config_option, which — see handleSetConfigOption's "model"
// case — currently refuses a live switch too, honestly, rather than
// pretending). A client can display the available engines and the launched
// one either way; only the requested model actually being HONORED at
// session start changed in this slice (see internal/claude/chat.go's
// claudeModelSelectionQuirk).
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

// ctxloomSessionInfoUpdate is ctxloom's own one-time session metadata (model,
// permission mode, context window, MCP server status) so a client can render
// a model/session header.
//
// IR4 (G13 accounting shapes): does the CURRENT spec (schema-v1.19.0) now
// settle a standard shape for this? Grepping the vendored schema
// (internal/acptest/acp-schema-v1.json, 142 $defs) for every candidate:
//   - SessionInfoUpdate (the real "session_info_update" variant, which the
//     L0 checklist B3 rename got ctxloom off of) is title/updatedAt ONLY —
//     confirmed unrelated to model/context/mcp, exactly as B3 found.
//   - UsageUpdate (the real "usage_update" variant) is used/size/cost — H1
//     already confirmed ctxloom's usage_update matches it exactly (see
//     usageUpdateWire); "size" duly covers the context-window GAUGE, so that
//     part of this header is already redundant with a foreign-renderable
//     frame today.
//   - SessionConfigOption's "model" category (CO1) is an ADVERTISEMENT of
//     available models, not a live resolved-model-for-this-turn echo, and
//     carries no permission-mode or MCP-status concept at all.
//   - No $defs entry anywhere models MCP connection status or a session's
//     live permission posture.
//
// VERDICT: NO — the spec does not settle Model+PermissionMode+MCPServers as
// a unit. DECISION: keep this as a vendor extension (per L0 checklist B3,
// still not the spec's own "session_info_update", which means something
// unrelated), but stop inventing a foreign top-level `sessionUpdate` variant
// name for it (that trips the spec's CLOSED SessionUpdate oneOf, which is a
// stricter failure mode than "unknown field" for a strict validating peer:
// nothing in the spec says a peer must tolerate an unrecognized
// discriminator the way it's obligated to tolerate an unrecognized `_meta`
// key). Instead this payload now rides as `_meta.ctxloom_session_info` on a
// REAL, spec-valid `session_info_update` frame (api.SessionSessionInfoUpdate
// — Title/UpdatedAt left unset since ctxloom has none to report) — see
// sessionInfoUpdateWire. `_meta` is the spec's OWN sanctioned extension
// channel ("Implementations MUST NOT make assumptions about values at these
// keys" — i.e. a conformant peer is contractually obligated to ignore it
// gracefully, not merely likely to). A foreign client now sees a harmless,
// schema-valid no-op title update instead of a construct it may reject
// outright; a ctxloom-aware client (internal/acp/mapping.go's
// sessionInfoVariant, redefined to "session_info_update" to match) still
// recovers the full header.
//
// NOT done this slice: riding the containing SessionNotification's OWN
// top-level `_meta` (sibling of `update`, see schema $defs/SessionNotification)
// was also considered — it would let this ride alongside content updates
// instead of needing its own frame at all. That would require adding a Meta
// field to sessionUpdateParams and threading it through the emission call
// sites, both in internal/acpagent/server.go, which is owned by concurrently
// running sibling slices this release (out of scope here — see this repo's
// IR4 brief). Deferred, not rejected.
type ctxloomSessionInfoUpdate struct {
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	ContextWindow  int             `json:"contextWindow,omitempty"`
	McpServers     []mcpStatusWire `json:"mcpServers,omitempty"`
}

// ctxloomSessionInfoMetaKey is the `_meta` object key this payload rides
// under (see ctxloomSessionInfoUpdate's doc comment). Mirrored by hand in
// internal/acp/mapping.go's sessionInfoMetaKey — the two packages don't share
// an import for this literal, exactly as the discriminator string itself was
// already mirrored by hand pre-IR4.
const ctxloomSessionInfoMetaKey = "ctxloom_session_info"

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

// CO1's config-option ids: ctxloom's own (unnamespaced) session-mutable
// surface. "profile" is the spec-general home for exactly what session/set_mode
// was repurposed to do (see switchProfile in server.go, shared by both
// surfaces); "model" is advertisement PLUS the session-start delivery fix
// (claudeModelSelectionQuirk) — see handleSetConfigOption's model case for
// why a LIVE mid-session switch is deliberately refused rather than silently
// accepted. Permission posture (D-CO's third knob) is NOT surfaced in this
// slice: unlike profile/model, ctxloom has no synchronous source for a
// session's launched permission posture at session/new time (it arrives
// later, per-event, via ChatSessionInfo) — advertising a value here would
// mean fabricating one, which this codebase's own no-silent-no-op standard
// forbids. See internal/claude/chat.go / internal/acp/session.go for the
// engine-crossing constraint that also blocks a live SET for both of these.
const (
	profileConfigID api.SessionConfigId = "profile"
	modelConfigID   api.SessionConfigId = "model"
)

// configOptionsWire renders the session's CURRENT config-option surface (both
// entries omitted when their underlying data is unavailable — never a
// fabricated placeholder). "profile" mirrors modeStateWire's data under the
// spec-general surface; "model" mirrors modelStateWire's data the same way.
// Called both to answer session/new|load|set_config_option's initial/echoed
// state, and to build the configOptionUpdate notification switchProfile
// fires on every change (COMPAT: alongside current_mode_update, never
// instead of it).
func configOptionsWire(modes *SessionModes, llms *SessionLLMs) []api.SessionConfigOption {
	// Always non-nil: SetSessionConfigOptionResponse.ConfigOptions has no
	// `omitempty` and its Validate() requires a non-nil (possibly empty)
	// array — a bare `nil` here would marshal as JSON `null`, which is not a
	// schema-valid array.
	out := make([]api.SessionConfigOption, 0, 2)
	if modes != nil && len(modes.Available) > 0 {
		modeCat := api.SessionConfigOptionCategoryMode
		opts := make(api.SessionConfigSelectOptionsUngrouped, 0, len(modes.Available))
		for _, m := range modes.Available {
			opts = append(opts, api.SessionConfigSelectOption{Value: api.SessionConfigValueId(m.ID), Name: m.Name})
		}
		out = append(out, api.SessionConfigOption{Select: &api.SessionConfigOptionSelect{
			Id:           profileConfigID,
			Name:         "Profile",
			Category:     &modeCat,
			CurrentValue: api.SessionConfigValueId(modes.Current),
			Options:      api.SessionConfigSelectOptions{Ungrouped: &opts},
		}})
	}
	if llms != nil && len(llms.Available) > 0 {
		modelCat := api.SessionConfigOptionCategoryModel
		opts := make(api.SessionConfigSelectOptionsUngrouped, 0, len(llms.Available))
		for _, m := range llms.Available {
			opts = append(opts, api.SessionConfigSelectOption{Value: api.SessionConfigValueId(m.ID), Name: m.Name})
		}
		out = append(out, api.SessionConfigOption{Select: &api.SessionConfigOptionSelect{
			Id:           modelConfigID,
			Name:         "Model",
			Category:     &modelCat,
			CurrentValue: api.SessionConfigValueId(llms.Current),
			Options:      api.SessionConfigSelectOptions{Ungrouped: &opts},
		}})
	}
	return out
}

// configOptionUpdateWire is the session/update variant announcing the FULL
// current config-option set after any one of them changes (the spec's
// ConfigOptionUpdate carries the whole set, not a delta — schema
// $defs/ConfigOptionUpdate).
func configOptionUpdateWire(opts []api.SessionConfigOption) any {
	return api.SessionUpdate{
		ConfigOptionUpdate: &api.SessionConfigOptionUpdate{ConfigOptions: opts},
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
