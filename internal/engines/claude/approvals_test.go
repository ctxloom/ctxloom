package claude

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The payloads below are the live shapes (claude 2.1.283, hookcheck cells A
// and D), trimmed to the members the codec reads plus a few it must ignore.
const (
	livePermissionRequest = `{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/w","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"description":"Create file a1","command":"touch a1"},"permission_suggestions":[{"type":"addDirectories","directories":["/w"],"destination":"session"},{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch *"},{"toolName":"Read"}],"behavior":"allow","destination":"localSettings"},{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"rm *"}],"behavior":"deny","destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`
)

func codec(t *testing.T) engine.ApprovalCodec {
	t.Helper()
	c, ok := Claude{}.Approvals().Get()
	require.True(t, ok, "claude provides its approval codec")
	return c
}

func TestApprovalCodec_DecodesAPermissionRequest(t *testing.T) {
	ask, err := codec(t).DecodeAsk(hookEventPermissionRequest, []byte(livePermissionRequest))
	require.NoError(t, err)
	assert.Equal(t, engine.AskTool, ask.Kind)
	assert.Equal(t, "Bash", ask.Tool)
	assert.JSONEq(t, `{"command":"touch a1","description":"Create file a1"}`, string(ask.Input))
	assert.Equal(t, `{"command":"touch a1","description":"Create file a1"}`, string(ask.Input), "the input is canonical: keys sorted")
	assert.Empty(t, ask.ToolUseID, "a PermissionRequest carries no tool_use_id")
	assert.Equal(t, []string{"Bash(touch *)", "Read"}, ask.Suggestions,
		"only allow-rule suggestions become grantable rules; directories and deny rules are not grants")
	mode, ok := ask.SuggestsSetMode.Get()
	require.True(t, ok)
	assert.Equal(t, modeAcceptEdits, mode)
}

func TestApprovalCodec_NeverSuggestsBypass(t *testing.T) {
	payload := `{"tool_name":"Bash","tool_input":{},"permission_suggestions":[{"type":"setMode","mode":"bypassPermissions","destination":"session"}]}`
	ask, err := codec(t).DecodeAsk(hookEventPermissionRequest, []byte(payload))
	require.NoError(t, err)
	_, ok := ask.SuggestsSetMode.Get()
	assert.False(t, ok, "a bypass suggestion is never offered")
}

func TestApprovalCodec_DecodeRefuses(t *testing.T) {
	c := codec(t)
	for name, tc := range map[string]struct{ event, payload string }{
		"unknown event":  {"Stop", livePermissionRequest},
		"not JSON":       {hookEventPermissionRequest, `{`},
		"no tool name":   {hookEventPermissionRequest, `{"tool_input":{}}`},
		"pre-tool event": {hookEventPreToolUse, livePermissionRequest},
	} {
		_, err := c.DecodeAsk(tc.event, []byte(tc.payload))
		assert.Errorf(t, err, "%s", name)
	}
}

// hookOut is the native answer, as claude reads it.
type hookOut struct {
	HookSpecificOutput struct {
		HookEventName string `json:"hookEventName"`
		Decision      *struct {
			Behavior           string           `json:"behavior"`
			Message            string           `json:"message"`
			UpdatedPermissions []map[string]any `json:"updatedPermissions"`
		} `json:"decision"`
	} `json:"hookSpecificOutput"`
}

func encode(t *testing.T, event string, ask engine.PermissionAsk, a engine.PermissionAnswer) hookOut {
	t.Helper()
	raw, err := codec(t).EncodeAnswer(event, ask, a)
	require.NoError(t, err)
	var out hookOut
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return out
}

func TestApprovalCodec_EncodesAnAllowForSession(t *testing.T) {
	out := encode(t, hookEventPermissionRequest, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}, engine.PermissionAnswer{
		Allow: true, SessionRules: []string{"Bash(touch *)", "Read"}, SetMode: engine.Provide(modeAcceptEdits),
	})
	assert.Equal(t, hookEventPermissionRequest, out.HookSpecificOutput.HookEventName)
	require.NotNil(t, out.HookSpecificOutput.Decision)
	assert.Equal(t, "allow", out.HookSpecificOutput.Decision.Behavior)
	assert.Equal(t, []map[string]any{
		{"type": "addRules", "behavior": "allow", "destination": "session", "rules": []any{
			map[string]any{"toolName": "Bash", "ruleContent": "touch *"}, map[string]any{"toolName": "Read"},
		}},
		{"type": "setMode", "mode": "acceptEdits", "destination": "session"},
	}, out.HookSpecificOutput.Decision.UpdatedPermissions)
}

func TestApprovalCodec_EncodesAPlainAllowAndADeny(t *testing.T) {
	allow := encode(t, hookEventPermissionRequest, engine.PermissionAsk{Kind: engine.AskTool}, engine.PermissionAnswer{Allow: true})
	assert.Equal(t, "allow", allow.HookSpecificOutput.Decision.Behavior)
	assert.Empty(t, allow.HookSpecificOutput.Decision.UpdatedPermissions)

	deny := encode(t, hookEventPermissionRequest, engine.PermissionAsk{Kind: engine.AskTool}, engine.PermissionAnswer{Message: "no"})
	assert.Equal(t, "deny", deny.HookSpecificOutput.Decision.Behavior)
	assert.Equal(t, "no", deny.HookSpecificOutput.Decision.Message)
}

func TestApprovalCodec_EncodeRefuses(t *testing.T) {
	c := codec(t)
	tool := engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}
	for name, tc := range map[string]struct {
		event string
		a     engine.PermissionAnswer
	}{
		"bypass":          {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(modeBypass)}},
		"plan":            {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(modePlan)}},
		"mode on a deny":  {hookEventPermissionRequest, engine.PermissionAnswer{SetMode: engine.Provide(modeDefault)}},
		"rules on a deny": {hookEventPermissionRequest, engine.PermissionAnswer{SessionRules: []string{"Bash"}}},
		"bad rule":        {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash("}}},
		"unknown event":   {"Stop", engine.PermissionAnswer{Allow: true}},
		"pre-tool event":  {hookEventPreToolUse, engine.PermissionAnswer{Allow: true}},
	} {
		_, err := c.EncodeAnswer(tc.event, tool, tc.a)
		assert.Errorf(t, err, "%s", name)
	}
}

// Property: whatever answer it is handed, an encoding the codec emits
// writes permissions to the session scope only, never names bypass, and
// changes mode only to accept-edits or default.
func TestApprovalCodec_EncodeProperty_SessionOnlyNoBypass(t *testing.T) {
	c := codec(t)
	rng := rand.New(rand.NewSource(1))
	modes := []string{"", modeDefault, modeAcceptEdits, modePlan, modeBypass, "dontAsk", "auto"}
	rules := []string{"Bash", "Bash(npm test)", "mcp__srv__tool", "Read(./src/**)", "WebFetch(domain:x.dev)"}
	emitted := 0
	for i := 0; i < 2000; i++ {
		a := engine.PermissionAnswer{Allow: rng.Intn(2) == 0, Message: "m"}
		for n := rng.Intn(3); n > 0; n-- {
			a.SessionRules = append(a.SessionRules, rules[rng.Intn(len(rules))])
		}
		if m := modes[rng.Intn(len(modes))]; m != "" {
			a.SetMode = engine.Provide(m)
		}
		raw, err := c.EncodeAnswer(hookEventPermissionRequest, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}, a)
		if err != nil {
			continue
		}
		emitted++
		s := string(raw)
		assert.NotContains(t, s, "bypass")
		var out hookOut
		require.NoError(t, json.Unmarshal(raw, &out))
		for _, p := range out.HookSpecificOutput.Decision.UpdatedPermissions {
			assert.Equal(t, "session", p["destination"], s)
			if p["type"] == "setMode" {
				assert.Contains(t, []any{"acceptEdits", "default"}, p["mode"], s)
			}
		}
		if !a.Allow {
			assert.Empty(t, out.HookSpecificOutput.Decision.UpdatedPermissions, "a deny carries no permissions: %s", s)
		}
	}
	assert.Greater(t, emitted, 200, "the property must actually exercise emitted encodings")
}

func TestApprovalCodec_RepoSurfaces(t *testing.T) {
	assert.ElementsMatch(t, []string{
		".claude/settings.json", ".claude/settings.local.json", ".mcp.json",
		".claude/skills/**", ".claude/agents/**", ".claude/commands/**",
	}, codec(t).RepoSurfaces())
}

func TestApprovalCodec_ValidateRule(t *testing.T) {
	c := codec(t)
	for _, ok := range []string{"Bash", "Bash(npm run test:*)", "Read(./src/**)", "WebFetch(domain:example.com)", "mcp__ctxloom", "mcp__ctxloom__search_content", "mcp__my-srv__do_it", "Bash(echo (hi))", "NotebookEdit"} {
		assert.NoErrorf(t, c.ValidateRule(ok), "%q", ok)
	}
	for _, bad := range []string{"", " ", "Bash(", "Bash()", "Bash( )", "(npm)", "Bash npm", "bash-tool", "Bash(x)y", "Bash(a\nb)", "mcp__", "9Bash"} {
		err := c.ValidateRule(bad)
		if assert.Errorf(t, err, "%q", bad) {
			assert.True(t, strings.Contains(err.Error(), "Tool(") || strings.Contains(err.Error(), "rule"), err.Error())
		}
	}
}

// TestApprovalCodec_HooksAreClaudesRouteToTheHuman: claude's approval hook
// is the permission ask for every tool its posture and rules left open
// (PermissionRequest, no matcher), running `ctxloom hook permission` and
// outliving the approval timeout. Nothing else is the route's: with no
// prompt tool, claude -p offers no question or plan to a pre-tool hook.
func TestApprovalCodec_HooksAreClaudesRouteToTheHuman(t *testing.T) {
	const approval = 15 * time.Minute
	h := approvalCodec{}.Hooks(approval)
	require.Len(t, h.PermissionAsk, 1)
	assert.Equal(t, agent.ApprovalHook("PermissionRequest", "", approval), h.PermissionAsk[0])
	assert.Len(t, h.All(), 1, "no other hook is the approval route's")
}
