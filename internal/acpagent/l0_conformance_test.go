package acpagent

// L0 — frame schema validation against the CURRENT ACP spec (internal/acptest,
// vendored schema-v1.19.0), deliberately newer than the pinned SDK
// (2025-09-02). This file captures every distinct frame SHAPE ctxloom emits
// in its AGENT role (internal/acpagent — the `ctxloom acp` server side) via
// the SAME in-process testClient/fakeEngine harness server_test.go already
// uses, and validates each captured payload against the spec's matching
// $defs entry.
//
// It is EXPECTED and CORRECT that some captured frames fail: that is the
// measurement this slice exists to produce, and it feeds directly into SDK1's
// acceptance checklist. Every expected failure is named in
// l0KnownDivergences with the exact substring its validation error must
// contain; a captured frame that is NOT in that map is asserted to PASS, so
// a NEW divergence (a regression, or the live spec moving further) fails
// this test immediately — the harness can never be silently tuned into
// measuring nothing (see this repo's "mutation gate truthfulness" incident).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acptest"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// l0Capture is one captured agent-emitted frame payload plus the current
// spec's $defs entry it should satisfy.
type l0Capture struct {
	label   string          // human label identifying the frame in a failure message
	defName string          // the current schema's $defs entry this payload is validated against
	payload json.RawMessage // the request/notification's params, or the response's result
}

// l0KnownDivergences enumerates every EXPECTED schema-validation failure this
// harness has confirmed against the CURRENT ACP spec (schema-v1.19.0). See
// the H1 harness report's "L0 DIVERGENCE LIST" for the full narrative
// (file:line of each emitter, why it diverges, what it means for SDK1).
// Each entry's value must be a substring of the validator's actual error; if
// a listed frame ever stops failing, require.Error below fails LOUDLY
// instead of silently starting to pass a check that no longer means what it
// used to — that is the signal to shrink this map, not to delete the test.
var l0KnownDivergences = map[string]string{
	// SDK1 (2026-07-16) deliberately renamed ctxloom's own session-info
	// extension off the spec's "session_info_update" wire name (L0 checklist
	// B3 — the current spec independently stabilized session_info_update as
	// unrelated session title/timestamp metadata; both shapes validated, so
	// the collision was invisible to schema checks, which is exactly why H1
	// flagged it as the headline actionable finding). Emitted now under
	// "ctxloom_session_info" (see internal/acpagent/wire.go's
	// ctxloomSessionInfoUpdate), a discriminator value the spec's CLOSED
	// SessionUpdate oneOf has no branch for — so it fails, on purpose: this
	// is ctxloom's own vendor extension, not a spec variant, and a
	// spec-conforming peer is expected to warn-and-drop it exactly like any
	// other unmodeled session/update (see mapping.go's default case).
	"session/update (ctxloom_session_info)": "'/update/sessionUpdate' does not validate with",
}

func mustValidator(t *testing.T) *acptest.Validator {
	t.Helper()
	v, err := acptest.NewValidator()
	require.NoError(t, err)
	return v
}

// checkL0 validates one captured frame: PASS unless its label is a known
// divergence, in which case it must fail with the recorded reason.
func checkL0(t *testing.T, v *acptest.Validator, c l0Capture) {
	t.Helper()
	err := v.ValidateDef(c.defName, c.payload)
	if want, known := l0KnownDivergences[c.label]; known {
		if !assert.Error(t, err, "%s (%s): expected a KNOWN divergence but validation now PASSES — shrink l0KnownDivergences", c.label, c.defName) {
			return
		}
		assert.Contains(t, err.Error(), want, "%s (%s): failed for a DIFFERENT reason than recorded — investigate before updating the allowlist", c.label, c.defName)
		return
	}
	assert.NoError(t, err, "%s (%s): NEW divergence from the current ACP schema (schema-v1.19.0), not in l0KnownDivergences", c.label, c.defName)
}

// TestL0_AgentEmittedFrames drives one comprehensive scripted conversation —
// handshake, session modes, a full turn with every entry kind (thinking,
// assistant text, tool use/result, permission forwarding), usage/session-info
// accounting, and a session/load resume with replay — capturing every
// distinct frame shape ctxloom emits as the ACP AGENT, then validates each
// against the current spec.
func TestL0_AgentEmittedFrames(t *testing.T) { //nolint:gocyclo // one linear scripted conversation is clearer than splitting an ordered handshake
	v := mustValidator(t)
	var captures []l0Capture
	capture := func(label, defName string, payload json.RawMessage) {
		captures = append(captures, l0Capture{label: label, defName: defName, payload: payload})
	}

	eng := newFakeEngine()
	eng.modes = &SessionModes{
		Current: DefaultModeID,
		Available: []SessionMode{
			{ID: DefaultModeID, Name: "default"},
			{ID: "review", Name: "review", Profiles: []string{"review"}},
		},
	}
	eng.assembleMode = func(context.Context, SessionMode) (string, error) { return "REVIEW CTX", nil }
	eng.llms = &SessionLLMs{Current: "primary", Available: []LLMInfo{{ID: "primary", Name: "primary"}}}
	go eng.pump()

	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat("LEAD CONTEXT"), nil })

	// initialize
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	capture("initialize response", "InitializeResponse", resp.Result)

	// session/new (with modes + models both populated)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	capture("session/new response", "NewSessionResponse", resp.Result)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	sid := newResp.SessionId

	// session/set_mode -> response + current_mode_update AND configOptionUpdate
	// notifications (CO1 COMPAT: switchProfile fires both surfaces on every
	// switch, regardless of which method triggered it).
	resp, updates := c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+sid+`","modeId":"review"}`))
	require.Nil(t, resp.Error)
	capture("session/set_mode response", "SetSessionModeResponse", resp.Result)
	require.Len(t, updates, 2)
	capture("current_mode_update", "SessionNotification", updates[0].Params)
	capture("config_option_update (from session/set_mode)", "SessionNotification", updates[1].Params)

	// CO1: session/set_config_option's "profile" case shares switchProfile's
	// mechanism with session/set_mode above — same COMPAT double-notify, the
	// other direction.
	resp, updates = c.waitResponse(c.send("session/set_config_option", `{"sessionId":"`+sid+`","configId":"profile","value":"default"}`))
	require.Nil(t, resp.Error)
	capture("session/set_config_option response (profile)", "SetSessionConfigOptionResponse", resp.Result)
	require.Len(t, updates, 2)
	capture("current_mode_update (from session/set_config_option)", "SessionNotification", updates[0].Params)
	capture("config_option_update", "SessionNotification", updates[1].Params)

	// CO1 D-CO-QUIRK: a LIVE mid-session "model" switch is honestly refused
	// (method-not-found), never a silent no-op — see handleSetConfigOption's
	// doc comment. Not schema-captured (it's a JSON-RPC error, not a result
	// payload), but exercised here so this scripted conversation proves the
	// refusal fires instead of silently succeeding.
	resp, _ = c.waitResponse(c.send("session/set_config_option", `{"sessionId":"`+sid+`","configId":"model","value":"haiku"}`))
	require.NotNil(t, resp.Error, "a live model switch must be refused loudly, never silently accepted")

	// A full turn: session info, thinking, assistant text, a tool call whose
	// permission is forwarded to the client mid-turn (session/updates keep
	// streaming while it's in flight — forwardPermission runs off the main
	// loop), the tool's result, then usage accounting.
	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"do work"}]}`)
	eng.receivedText(t)
	eng.events <- agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: "test-model", ContextWindow: 100000, MCPServers: []agent.MCPStatus{{Name: "tools", Status: "connected"}}}}
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}}
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "working on it"}}
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "fs_read", ToolInput: json.RawMessage(`{"path":"x"}`)}}
	eng.events <- agent.ChatEvent{Permission: &agent.PermissionRequest{
		ID: "perm-1", ToolName: "fs_read",
		Options: []agent.PermissionOption{{ID: "ok", Kind: "allow_once", Name: "Allow"}, {ID: "no", Kind: "reject_once", Name: "Reject"}},
	}}

	permReq := c.waitRequest("session/request_permission")
	capture("session/request_permission request", "RequestPermissionRequest", permReq.Params)
	c.respond(permReq, `{"outcome":{"outcome":"selected","optionId":"ok"}}`)

	eng.receiveMsg(t) // the permission answer reaches the engine
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolOutput: "data"}}
	// IR2 headline: a plan entry (SystemKind==SystemKindPlan, structured
	// entries) must re-emit a REAL ACP `plan` update — schema-validated below
	// like every other captured frame, not a special-cased assertion.
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeSystem, SystemKind: agent.SystemKindPlan,
		Plan: []agent.PlanEntry{
			{Content: "Read the file", Priority: "high", Status: "completed"},
			{Content: "Apply the fix", Priority: "medium", Status: "in_progress"},
		},
	}}
	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{InputTokens: 1234, ContextWindow: 100000, CostUSD: 0.02, StopReason: "end_turn"}}

	resp, turnUpdates := c.waitResponse(id)
	require.Nil(t, resp.Error)
	capture("session/prompt response", "PromptResponse", resp.Result)
	require.GreaterOrEqual(t, len(turnUpdates), 5, "ctxloom_session_info, thinking, assistant, tool_call, tool_call_update, plan, usage_update")
	labels := []string{"ctxloom_session_info", "agent_thought_chunk", "agent_message_chunk", "tool_call", "tool_call_update", "plan", "usage_update"}
	var planUpdateIdx = -1
	for i, u := range turnUpdates {
		label := "session/update"
		if i < len(labels) {
			label = "session/update (" + labels[i] + ")"
			if labels[i] == "plan" {
				planUpdateIdx = i
			}
		}
		capture(label, "SessionNotification", u.Params)
	}
	require.GreaterOrEqual(t, planUpdateIdx, 0, "the plan update must have been captured")
	// Headline proof: the actual wire bytes carry the structured plan
	// entries, not a flattened checklist string.
	planFrame := string(turnUpdates[planUpdateIdx].Params)
	assert.Contains(t, planFrame, `"sessionUpdate":"plan"`)
	assert.Contains(t, planFrame, `"content":"Read the file"`)
	assert.Contains(t, planFrame, `"priority":"high"`)
	assert.Contains(t, planFrame, `"status":"completed"`)
	assert.Contains(t, planFrame, `"content":"Apply the fix"`)
	assert.Contains(t, planFrame, `"status":"in_progress"`)
	t.Logf("plan frame on the wire: %s", planFrame)

	// session/request_permission with ZERO options — the confirmed edge-case
	// divergence (see l0KnownDivergences).
	go func() {
		eng.receivedText(t)
		eng.events <- agent.ChatEvent{Permission: &agent.PermissionRequest{ID: "perm-empty", ToolName: "no_opts"}}
	}()
	id2 := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"trigger empty-options permission"}]}`)
	emptyOptsReq := c.waitRequest("session/request_permission")
	capture("session/request_permission request, engine supplied zero options", "RequestPermissionRequest", emptyOptsReq.Params)
	c.respond(emptyOptsReq, `{"outcome":{"outcome":"cancelled"}}`)
	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ = c.waitResponse(id2)
	require.Nil(t, resp.Error)

	// session/load on a FRESH server/session: replay entries (user +
	// assistant) arrive as session/update notifications before the response.
	eng2 := newFakeEngine()
	eng2.replay = []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "earlier question"},
		{Type: agent.EntryTypeAssistant, Content: "earlier answer"},
	}
	go eng2.pump()
	c2 := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng2.chat("RESUME CTX"), nil })
	c2.waitResponse(c2.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	loadID := c2.send("session/load", `{"sessionId":"resumed-harp","cwd":"/proj","mcpServers":[]}`)
	resp, loadUpdates := c2.waitResponse(loadID)
	require.Nil(t, resp.Error)
	capture("session/load response", "LoadSessionResponse", resp.Result)
	require.Len(t, loadUpdates, 2)
	capture("session/load replay (user_message_chunk)", "SessionNotification", loadUpdates[0].Params)
	capture("session/load replay (agent_message_chunk)", "SessionNotification", loadUpdates[1].Params)

	require.NotEmpty(t, captures, "the scripted conversation must have captured at least one frame — an empty capture list would validate nothing and certify emptiness as success")
	for _, cap := range captures {
		checkL0(t, v, cap)
	}
}

// TestL0_KnownDivergenceSubstringsAreDistinct is a light meta-test: it
// confirms l0KnownDivergences isn't accidentally aliasing two different real
// failure reasons under a shared substring (which would let a genuinely new
// divergence hide behind an old one's expected text).
func TestL0_KnownDivergenceSubstringsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for label, want := range l0KnownDivergences {
		if prior, ok := seen[want]; ok {
			t.Errorf("l0KnownDivergences: %q and %q share the identical expected substring %q — a new, different divergence could hide behind either", label, prior, want)
		}
		seen[want] = label
		require.NotEmpty(t, strings.TrimSpace(want), "%s: an empty expected substring would match ANY error, defeating the check", label)
	}
}
