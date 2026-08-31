package acp

// L0 — frame schema validation against the CURRENT ACP spec (internal/acptest,
// vendored schema-v1.19.0), deliberately newer than the pinned SDK, which is
// frozen (see doc.go's SDK decision section). This file is the CLIENT-role twin of
// internal/acpagent/l0_conformance_test.go: it captures every distinct frame
// shape ctxloom emits as an ACP CLIENT (internal/acp — driving another
// engine's `<agent> acp`) via the SAME in-process fakeAgent harness
// fakeagent_test.go/execute_test.go already use, and validates each captured
// payload against the spec's matching $defs entry.
//
// A captured frame not listed in l0ClientKnownDivergences is asserted to
// PASS; any listed divergence must fail with its recorded reason — see
// internal/acpagent/l0_conformance_test.go's doc comment for the full
// rationale (identical policy, client side).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acptest"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

type l0ClientCapture struct {
	label   string
	defName string
	payload json.RawMessage
}

// l0ClientKnownDivergences — see internal/acpagent/l0_conformance_test.go's
// l0KnownDivergences for the shared policy this mirrors.
//
// SDK1 fixed the L0 checklist's A1 (fs/write_text_file used to
// return a bare nil, rendering as JSON `null`, which failed the spec's
// `"type":"object"` WriteTextFileResponse — see
// internal/acp/session.go's handleFsWrite, which now returns
// api.WriteTextFileResponse{}): the divergence that used to be recorded
// here is GONE, confirmed by this test itself failing with "validation now
// PASSES" until the entry was removed.
//
// The map went back to non-empty when L0 switched from acptest.NewValidator
// to acptest.NewStrictValidator: as vendored, schema-v1.19.0
// closes no object shape, so the harness could see a MISSING required field
// but never an EXTRA one. The strict Validator measures that second half, and
// found one thing on this side.
var l0ClientKnownDivergences = map[string]string{
	// DIVERGENCE 1 — `clientCapabilities.auth` on the initialize request.
	//
	// WHAT: setup (internal/acp/session.go) fills an SDK
	// api.ClientCapabilities, and that struct carries an `Auth
	// AuthCapabilities` field tagged `json:"auth,omitempty"`. omitempty does
	// nothing for a struct value, so `"auth":{}` rides EVERY initialize
	// request ctxloom sends as a client. schema-v1.19.0's ClientCapabilities
	// defines fs, terminal, session and _meta — there is no `auth`.
	//
	// WHOSE FIELD IT IS: not ctxloom's. The SDK's own doc comment on that
	// field says "**UNSTABLE** — This capability is not part of the spec yet,
	// and may be removed or changed at any point". ctxloom never sets it; it
	// is the zero value of a field the SDK always serialises. This is exactly
	// the SDK-vs-current-spec gap this harness was built to measure, arriving
	// from the direction nobody was watching.
	//
	// WHEN THIS ENTRY COMES OFF: when the SDK stops emitting the field (a
	// pointer type, a working omitempty, or removal), or when upstream adopts
	// `auth` into ClientCapabilities and a re-vendor brings it in. Either way
	// checkL0Client fails loudly at that moment rather than letting a stale
	// entry ride.
	"initialize request": "'/clientCapabilities/auth' does not validate",
}

func mustClientValidator(t *testing.T) *acptest.Validator {
	t.Helper()
	v, err := acptest.NewStrictValidator()
	require.NoError(t, err)
	return v
}

func checkL0Client(t *testing.T, v *acptest.Validator, c l0ClientCapture) {
	t.Helper()
	err := v.ValidateDef(c.defName, c.payload)
	if want, known := l0ClientKnownDivergences[c.label]; known {
		if !assert.Error(t, err, "%s (%s): expected a KNOWN divergence but validation now PASSES — shrink l0ClientKnownDivergences", c.label, c.defName) {
			return
		}
		assert.Contains(t, err.Error(), want, "%s (%s): failed for a DIFFERENT reason than recorded — investigate before updating the allowlist", c.label, c.defName)
		return
	}
	assert.NoError(t, err, "%s (%s): NEW divergence from the current ACP schema (schema-v1.19.0), not in l0ClientKnownDivergences", c.label, c.defName)
}

// l0CallClient sends an arbitrary agent->client request over the fakeAgent's
// connection and blocks for the response — the same request/response
// pattern fa.requestPermission (fakeagent_test.go) already uses for
// session/request_permission, generalized here (without modifying that file)
// so fs/read_text_file and fs/write_text_file can be driven the same way.
func l0CallClient(a *fakeAgent, method string, params any) rpcMessage {
	id := atomic.AddInt64(&a.nextID, 1)
	ch := make(chan rpcMessage, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()
	raw, _ := json.Marshal(params)
	_ = a.writeFrame(rpcMessage{Method: method, ID: json.RawMessage(strconv.FormatInt(id, 10)), Params: raw})
	select {
	case resp := <-ch:
		return resp
	case <-time.After(10 * time.Second):
		panic("l0CallClient: timed out waiting for " + method + " response")
	}
}

// TestL0_ClientEmittedFrames drives one comprehensive scripted conversation —
// handshake, a turn cancelled mid-flight (capturing session/prompt AND
// session/cancel), an auto-decided permission request, and both fs/*
// callbacks — capturing every distinct frame shape ctxloom emits as an ACP
// CLIENT, then validates each against the current spec.
func TestL0_ClientEmittedFrames(t *testing.T) {
	v := mustClientValidator(t)
	var captures []l0ClientCapture
	capture := func(label, defName string, payload json.RawMessage) {
		captures = append(captures, l0ClientCapture{label: label, defName: defName, payload: payload})
	}

	b := NewACP()
	fa := executeHarness(t, b)

	// A REAL workspace directory, not the fictional "/proj" this used to
	// pass: the fs confinement (fsconfine.go) resolves every fs/* path
	// against it, and a root that does not exist denies (fail closed). The
	// two fs/* captures below therefore live inside it.
	workspace := t.TempDir()

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 32)
	chatDone := make(chan error, 1)
	go func() {
		chatDone <- b.Chat(context.Background(), agent.ChatRequest{
			WorkDir: workspace,
			// ForwardPermissions is deliberately UNSET: the driver forwards
			// session/request_permission unconditionally, so the permission
			// capture below needs a live upstream consumer either way — the
			// out-drain goroutine below plays it.
			MCPServers: []agent.ChatMCPServer{
				{Name: "tools", Command: "/bin/tools"},
				// An http entry, accepted because the fake
				// agent's initialize response below advertises
				// mcpCapabilities.http — this proves mcpServersToACP's
				// constructed McpServerHttpInline is schema-valid, not just
				// that stdio still is.
				{Name: "remote-tools", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
			},
		}, in, out)
	}()
	go func() {
		for ev := range out { // drain — this test measures EMISSION, not the client's own decoding of inbound updates
			// ...except a permission, which this stand-in upstream must
			// ANSWER: ctxloom never decides one itself, so an unanswered
			// request parks the fake agent forever and the capture below never
			// happens. The send is ordered strictly before close(in) because
			// fa.requestPermission does not return until this answer lands.
			if ev.Permission != nil {
				in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: ev.Permission.ID, OptionID: "ok"}}
			}
		}
	}()

	// initialize
	initReq := <-fa.requests
	require.Equal(t, "initialize", initReq.Method)
	capture("initialize request", "InitializeRequest", initReq.Params)
	require.NoError(t, fa.respond(initReq.ID, map[string]any{
		"protocolVersion":   1,
		"agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": true}},
		"authMethods":       []any{},
	}))

	// session/new
	newReq := <-fa.requests
	require.Equal(t, "session/new", newReq.Method)
	capture("session/new request", "NewSessionRequest", newReq.Params)
	const sid = "sess-l0"
	require.NoError(t, fa.respond(newReq.ID, map[string]any{"sessionId": sid}))

	// session/prompt, left UNANSWERED so the caller's CancelTurn produces a
	// real session/cancel notification (Chat only cancels a turn actually
	// in flight).
	in <- agent.ChatMessage{Text: "hello"}
	promptReq := <-fa.requests
	require.Equal(t, "session/prompt", promptReq.Method)
	capture("session/prompt request", "PromptRequest", promptReq.Params)

	in <- agent.ChatMessage{CancelTurn: true}
	cancelNotif := <-fa.notifications
	require.Equal(t, "session/cancel", cancelNotif.Method)
	capture("session/cancel notification", "CancelNotification", cancelNotif.Params)
	require.NoError(t, fa.respond(promptReq.ID, map[string]any{"stopReason": "cancelled"}))

	// fs/read_text_file (agent -> client -> real filesystem)
	tmpFile := filepath.Join(workspace, "f.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("hello\n"), 0o644))
	readResp := l0CallClient(fa, "fs/read_text_file", map[string]any{"path": tmpFile})
	require.Nil(t, readResp.Error)
	capture("fs/read_text_file response", "ReadTextFileResponse", readResp.Result)

	// fs/write_text_file — the confirmed divergence.
	writeResp := l0CallClient(fa, "fs/write_text_file", map[string]any{"path": filepath.Join(workspace, "w.txt"), "content": "written"})
	require.Nil(t, writeResp.Error)
	capture("fs/write_text_file response", "WriteTextFileResponse", writeResp.Result)

	// session/request_permission -> RequestPermissionResponse (forwarded
	// upstream and answered by the out-drain goroutine above, which selects
	// "ok").
	permResp := fa.requestPermission(sid, []map[string]any{
		{"optionId": "ok", "kind": "allow_once", "name": "Allow"},
		{"optionId": "no", "kind": "reject_once", "name": "Reject"},
	})
	require.Nil(t, permResp.Error)
	capture("session/request_permission response", "RequestPermissionResponse", permResp.Result)

	close(in)
	select {
	case err := <-chatDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Chat to return after closing input")
	}

	require.NotEmpty(t, captures, "the scripted conversation must have captured at least one frame — an empty capture list would validate nothing and certify emptiness as success")
	for _, cap := range captures {
		checkL0Client(t, v, cap)
	}
}

// TestL0_ClientEmittedFrames_Multimodal is the L0 proof: when the connected
// engine advertises image/audio/embeddedContext support, the session/prompt
// request ctxloom actually SENDS carries real ImageBlock/AudioBlock/Resource
// content (buildPromptBlocks/deliverBlock) — and that request is still a
// schema-valid PromptRequest under the current spec. A capability-gated
// degradation (the engine did NOT advertise support) is exercised in
// TestBuildPromptBlocks_Degrades (session_test.go) with a payload assertion
// on the flattened placeholder text; this test's job is narrower: prove the
// UNDEGRADED path's wire shape validates.
func TestL0_ClientEmittedFrames_Multimodal(t *testing.T) {
	v := mustClientValidator(t)

	b := NewACP()
	fa := executeHarness(t, b)

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 32)
	chatDone := make(chan error, 1)
	go func() {
		chatDone <- b.Chat(context.Background(), agent.ChatRequest{WorkDir: "/proj"}, in, out)
	}()
	go func() {
		for range out {
		}
	}()

	initReq := <-fa.requests
	require.NoError(t, fa.respond(initReq.ID, map[string]any{
		"protocolVersion": 1,
		"agentCapabilities": map[string]any{
			"promptCapabilities": map[string]any{"image": true, "audio": true, "embeddedContext": true},
		},
	}))
	newReq := <-fa.requests
	require.NoError(t, fa.respond(newReq.ID, map[string]any{"sessionId": "sess-mm"}))

	in <- agent.ChatMessage{
		Text: "describe this",
		ContentBlocks: []agent.ContentBlock{
			{Kind: "text", Text: "describe this", Raw: json.RawMessage(`{"type":"text","text":"describe this"}`)},
			{Kind: "image", Raw: json.RawMessage(`{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}`)},
			{Kind: "audio", Raw: json.RawMessage(`{"type":"audio","data":"d29ybGQ=","mimeType":"audio/wav"}`)},
		},
	}
	promptReq := <-fa.requests
	require.Equal(t, "session/prompt", promptReq.Method)

	// Payload assertion: the real bytes reached the wire, not a placeholder.
	assert.Contains(t, string(promptReq.Params), `"aGVsbG8="`, "the image's real data reached the engine")
	assert.Contains(t, string(promptReq.Params), `"d29ybGQ="`, "the audio's real data reached the engine")
	assert.NotContains(t, string(promptReq.Params), "not delivered", "an advertised-capable engine gets the real block, never the flatten-with-warning placeholder")
	checkL0Client(t, v, l0ClientCapture{label: "session/prompt request (multimodal, capable engine)", defName: "PromptRequest", payload: promptReq.Params})

	require.NoError(t, fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	close(in)
	select {
	case err := <-chatDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Chat to return")
	}
}
