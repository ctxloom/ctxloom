package acpagent

import (
	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file carries the ACP wire shapes that are NOT plain pass-throughs of
// the coder/acp-go-sdk (fork) wire types: the modes/models state riding session/new and
// session/load's response, and ctxloom's own (renamed, non-colliding)
// session-info extension — which IR4 moved off its own bespoke top-level
// `sessionUpdate` variant onto the spec's `_meta` extension channel riding a
// REAL `session_info_update` frame, and which G13 then DECOMPOSED: two of
// its four facts (Model, ContextWindow) turned out to already have a real
// spec home ctxloom wasn't using (SessionConfigOption's "model" category and
// UsageUpdate's `size` respectively) and were moved there; only
// PermissionMode and MCP connection status remain genuinely homeless and
// still ride `_meta` (see ctxloomSessionInfoUpdate's doc comment for the
// full per-fact evidence). The initialize response's agentInfo and the
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
//
// U014-F11 RETIREMENT TRIGGER (recorded, not yet met): drop the `models`
// field from this struct and loadSessionResult, and delete modelState/
// modelWire/modelStateWire entirely, once BOTH named target clients
// (agentic.nvim, formulahendry's picker) speak session/set_config_option's
// spec-general "model" category config option in a released version — or,
// failing a confirmed release date from either, on the next deliberate ACP
// compat-surface review. Until then this is duplicated wire surface with a
// known, not open-ended, removal condition.
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

// ctxloomSessionInfoUpdate is ctxloom's own one-time session metadata that a
// FOREIGN client (zero ctxloom knowledge) has no other way to receive:
// permission posture and per-server MCP connection status.
//
// G13 (correcting IR4): IR4 asked "does the spec settle a shape for this
// COMBINED header?", found no — correctly — and rode the WHOLE four-field
// blob (Model, PermissionMode, ContextWindow, McpServers) in `_meta`. That
// made every frame schema-valid but meant NO foreign client renders ANY of
// it, which was G13's actual goal. The fix is not a better `_meta` home for
// the blob — it's to stop treating it as one blob: two of these four facts
// already have a REAL spec home ctxloom just wasn't using.
//
//   - Model: HAS a spec home. SessionConfigOption's "select" variant
//     (schema $defs/SessionConfigOptionSelect) carries a `currentValue`
//     field alongside its `options` — IR4's shallow property dump of
//     SessionConfigOption ([id, name, description, category, _meta]) missed
//     this because SessionConfigOption is a union (oneOf Select/Boolean,
//     types_gen.go's SessionConfigOption.Select/.Boolean) and the dump
//     apparently inspected only the shared/base shape. CO1 already emits a
//     `category: "model"` SessionConfigOption at session/new, session/load,
//     set_config_option's response, AND a config_option_update notification
//     on every change (see configOptionsWire's "model" entry, CurrentValue:
//     llms.Current) — a foreign client already renders the CURRENT model
//     from THAT, unconditionally, today. Model is therefore DROPPED from
//     this struct: it was pure duplication of a channel that was already
//     spec-shaped and already firing.
//   - ContextWindow: HAS a spec home, confirmed by H1: UsageUpdate (the
//     real "usage_update" variant) carries `size` ("Total context window
//     size in tokens") alongside `used` — see usageUpdateWire, which
//     already emits this exactly. ContextWindow is DROPPED from this struct
//     for the same reason as Model: it was duplicating a frame that was
//     already correct and already firing. (Timing note, not a spec gap: the
//     session-start header used to show the window before any turn ran;
//     usage_update only fires once a turn COMPLETES, so a foreign client's
//     first view of the window gauge is now after turn 1, not at session
//     start — see this file's package doc and the G13 report for the full
//     honesty on this tradeoff.)
//   - PermissionMode: NO spec home. Exhaustive grep of the vendored schema
//     (internal/acptest/acp-schema-v1.json, 142 $defs) for every
//     permission-adjacent construct finds only the per-tool-call approval
//     flow (RequestPermissionRequest/Response, PermissionOption,
//     SelectedPermissionOutcome) — none of it models a session's ongoing
//     permission POSTURE. SessionConfigOptionCategory's own enum is closed
//     to {mode, model, model_config, thought_level, <custom "_"-prefixed>}
//     — no "permission" category exists. The one textual near-miss,
//     SetSessionModeRequest's doc ("modes... affect... permission
//     behaviors"), describes what an engine's OWN modes MAY encode, not a
//     separate construct — and ctxloom's `mode`/`SessionConfigOption`
//     "mode"-category slot is already spent on PROFILE switching (see CO1
//     and switchProfile), not permission posture, which this slice does not
//     reopen. PermissionMode STAYS here.
//   - McpServers (per-server connection status): NO spec home. The schema's
//     only Mcp* $defs (McpServer/McpServerHttp/McpServerSse/McpServerStdio,
//     McpCapabilities) are the CLIENT's input declaration of which servers
//     to connect (session/new's `mcpServers` param) and capability
//     negotiation — there is no construct anywhere for the AGENT to report
//     a server's live connection status back. McpServers STAYS here.
//
// So this struct still rides as a vendor extension inside a REAL,
// spec-valid `session_info_update` frame's `_meta` object
// (api.SessionSessionInfoUpdate — Title/UpdatedAt left unset since ctxloom
// has none to report), exactly as IR4 wired it — see sessionInfoUpdateWire —
// but now carries only the two facts that are genuinely homeless, not the
// two that already had a better address. `_meta` is the spec's OWN
// sanctioned extension channel ("Implementations MUST NOT make assumptions
// about values at these keys"), so a foreign client still harmlessly
// ignores this residue as a no-op title update; the client-side twin
// (internal/acp/mapping.go, internal/acp/session.go's consumeMetaUpdate) no
// longer has any special case for this discriminator at all — Model/
// ContextWindow consumption is REMOVED there, not left silently no-op'd
// (see that file's doc comments for the full G13 rationale).
type ctxloomSessionInfoUpdate struct {
	PermissionMode string          `json:"permissionMode,omitempty"`
	McpServers     []mcpStatusWire `json:"mcpServers,omitempty"`
}

// ctxloomSessionInfoMetaKey is the `_meta` object key this payload rides
// under (see ctxloomSessionInfoUpdate's doc comment). Mirrored by hand as a
// literal in internal/acp/session_test.go's test fixtures (the client side
// no longer decodes this key at all post-G13, so there is no production
// constant to share it with) — same hand-mirroring the discriminator string
// itself already needed pre-IR4.
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
