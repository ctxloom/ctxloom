package conformance

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// conformanceApprovalTimeout is the approval timeout the contract asks an
// engine's codec for hooks under.
const conformanceApprovalTimeout = 7 * time.Minute

// notAPosture is a posture no engine offers: an answer must never move a
// session to it.
const notAPosture = "ctxloom-conformance-not-a-posture"

// ApprovalCodec asserts the codec-level approval contract every engine that
// declares an ApprovalCodec must meet, in ctxloom's neutral vocabulary only —
// no engine wire format, so it holds for every engine alike:
//
//   - its hooks carry asks and nothing else: each one is ctxloom's approval
//     hook (agent.ApprovalHook — exec form, `ctxloom hook permission --event
//     <event>`) on the permission-ask event, outliving the approval timeout;
//   - for each hook's event, a payload that is not one (unreadable, or naming
//     no tool) decodes to an error, never an ask;
//   - an allow and a deny encode; a deny carries no session rules and no mode
//     change; an allow may change mode only to a posture the engine's model
//     offers (PermissionModel.Transitions);
//   - a blank rule and a multi-line rule are not rules.
//
// The end-to-end half — the engine's real hook reaching the human at the
// root and the decision returning — is the PermissionContract lane under
// tests/, which may cross layers.
func ApprovalCodec(t *testing.T, eng engine.Engine) {
	t.Helper()
	codec, ok := eng.Approvals().Get()
	if !ok {
		return
	}
	hooks := codec.Hooks(conformanceApprovalTimeout)
	require.NotEmpty(t, hooks.PermissionAsk, "an approval codec carries at least one ask hook")
	require.Equal(t, hooks.PermissionAsk, hooks.All(), "the approval route's hooks carry asks and nothing else")
	for _, h := range hooks.PermissionAsk {
		require.Len(t, h.Args, 4, "an approval hook is ctxloom's own, in exec form: %+v", h)
		event := h.Args[3]
		require.Equal(t, agent.ApprovalHook(event, h.Matcher, conformanceApprovalTimeout), h,
			"an approval hook runs `ctxloom hook permission --event <event>` in exec form and outlives the approval timeout")
		checkAskDecoding(t, codec, event)
		checkAnswerEncoding(t, eng, codec, event)
	}
	require.Error(t, codec.ValidateRule(""), "a blank rule is not a rule")
	require.Error(t, codec.ValidateRule("Bash\nRead"), "a rule is one line")
}

// checkAskDecoding: a payload that is not an ask is an error, never an ask.
func checkAskDecoding(t *testing.T, codec engine.ApprovalCodec, event string) {
	t.Helper()
	for _, payload := range []string{`{`, `{}`} {
		_, err := codec.DecodeAsk(event, []byte(payload))
		require.Error(t, err, "event %q payload %s names no tool call: it decodes to no ask", event, payload)
	}
}

// checkAnswerEncoding: an allow and a deny encode; a deny grants nothing;
// a mode change goes only where the engine's model offers.
func checkAnswerEncoding(t *testing.T, eng engine.Engine, codec engine.ApprovalCodec, event string) {
	t.Helper()
	ask := engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{}`)}
	encodes := func(a engine.PermissionAnswer, why string) {
		t.Helper()
		out, err := codec.EncodeAnswer(event, ask, a)
		require.NoError(t, err, why)
		require.NotEmpty(t, out, why)
	}
	refuses := func(a engine.PermissionAnswer, why string) {
		t.Helper()
		_, err := codec.EncodeAnswer(event, ask, a)
		require.Error(t, err, why)
	}
	encodes(engine.PermissionAnswer{Allow: true}, "an allow encodes")
	encodes(engine.PermissionAnswer{Message: "no"}, "a deny encodes")
	refuses(engine.PermissionAnswer{SessionRules: []string{"Bash"}}, "a deny carries no session rules")
	refuses(engine.PermissionAnswer{SetMode: engine.Provide("default")}, "a deny carries no mode change")
	refuses(engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(notAPosture)}, "an allow changes mode only to a posture the engine offers")
	if model, ok := eng.Permissions().Get(); ok {
		for _, tr := range model.Transitions(nil) {
			encodes(engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(tr.Posture)}, "an allow may change mode to an offered posture: "+tr.Posture)
		}
	}
}
