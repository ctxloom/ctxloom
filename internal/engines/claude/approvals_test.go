package claude

import (
	"encoding/json"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The payloads below are the live shapes (claude 2.1.283, hookcheck cells A
// and D), trimmed to the members the codec reads plus a few it must ignore.
const (
	livePermissionRequest = `{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/w","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"description":"Create file a1","command":"touch a1"},"permission_suggestions":[{"type":"addDirectories","directories":["/w"],"destination":"session"},{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch *"},{"toolName":"Read"}],"behavior":"allow","destination":"localSettings"},{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"rm *"}],"behavior":"deny","destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`
	liveAskQuestion       = `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which color should the file contain?","header":"Color choice","options":[{"label":"red","description":"Choose red as the file content"},{"label":"blue","description":"Choose blue as the file content"}],"multiSelect":false}]},"tool_use_id":"toolu_01DviSddxv196p1XDfwBUREg"}`
	liveExitPlan          = `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan\n\nRun ` + "`touch c1`" + `.\n","planFilePath":"/home/u/.claude/plans/p.md"},"tool_use_id":"toolu_01HKCaGnUQaH15bCRh51MGBp"}`
	liveHostCall          = `{"tool_name":"ExitPlanMode","input":{"plan":"# Plan","planFilePath":"/p.md"},"tool_use_id":"toolu_01HKCaGnUQaH15bCRh51MGBp"}`
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

func TestApprovalCodec_DecodesAQuestion(t *testing.T) {
	ask, err := codec(t).DecodeAsk(hookEventPreToolUse, []byte(liveAskQuestion))
	require.NoError(t, err)
	assert.Equal(t, engine.AskQuestion, ask.Kind)
	assert.Equal(t, "toolu_01DviSddxv196p1XDfwBUREg", ask.ToolUseID)
	require.Len(t, ask.Questions, 1)
	assert.Equal(t, engine.Question{
		Header: "Color choice", Text: "Which color should the file contain?",
		Options: []engine.QuestionOption{{Label: "red", Description: "Choose red as the file content"}, {Label: "blue", Description: "Choose blue as the file content"}},
	}, ask.Questions[0])
}

func TestApprovalCodec_DecodesAPlan(t *testing.T) {
	ask, err := codec(t).DecodeAsk(hookEventPreToolUse, []byte(liveExitPlan))
	require.NoError(t, err)
	assert.Equal(t, engine.AskPlan, ask.Kind)
	require.NotNil(t, ask.Plan)
	assert.Equal(t, engine.PlanProposal{Markdown: "# Plan\n\nRun `touch c1`.\n", Path: "/home/u/.claude/plans/p.md"}, *ask.Plan)
}

func TestApprovalCodec_DecodeRefuses(t *testing.T) {
	c := codec(t)
	for name, tc := range map[string]struct{ event, payload string }{
		"unknown event":  {"Stop", livePermissionRequest},
		"not JSON":       {hookEventPermissionRequest, `{`},
		"no tool name":   {hookEventPermissionRequest, `{"tool_input":{}}`},
		"pretool no id":  {hookEventPreToolUse, `{"tool_name":"AskUserQuestion","tool_input":{"questions":[]}}`},
		"plan not plan":  {hookEventPreToolUse, `{"tool_name":"ExitPlanMode","tool_input":{"plan":7},"tool_use_id":"x"}`},
		"questions junk": {hookEventPreToolUse, `{"tool_name":"AskUserQuestion","tool_input":{"questions":"x"},"tool_use_id":"x"}`},
	} {
		_, err := c.DecodeAsk(tc.event, []byte(tc.payload))
		assert.Errorf(t, err, "%s", name)
	}
}

// hookOut is the native answer, as claude reads it.
type hookOut struct {
	HookSpecificOutput struct {
		HookEventName            string          `json:"hookEventName"`
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		Decision                 *struct {
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

// The answered question echoes the input with the answers keyed by the
// question text (LIVE-D1): claude rejects an allow without updatedInput.
func TestApprovalCodec_EncodesAnAnsweredQuestion(t *testing.T) {
	ask, err := codec(t).DecodeAsk(hookEventPreToolUse, []byte(liveAskQuestion))
	require.NoError(t, err)
	out := encode(t, hookEventPreToolUse, ask, engine.PermissionAnswer{Allow: true, Answers: []engine.QuestionAnswer{
		{Question: "Which color should the file contain?", Labels: []string{"red", "blue"}, Other: "teal"},
	}})
	assert.Equal(t, "allow", out.HookSpecificOutput.PermissionDecision)
	var in map[string]any
	require.NoError(t, json.Unmarshal(out.HookSpecificOutput.UpdatedInput, &in))
	assert.Equal(t, map[string]any{"Which color should the file contain?": "red, blue, teal"}, in["answers"])
	assert.NotNil(t, in["questions"], "the questions are echoed")
}

func TestApprovalCodec_EncodesAPlanDecision(t *testing.T) {
	ask, err := codec(t).DecodeAsk(hookEventPreToolUse, []byte(liveExitPlan))
	require.NoError(t, err)
	approve := encode(t, hookEventPreToolUse, ask, engine.PermissionAnswer{Allow: true})
	assert.Equal(t, "allow", approve.HookSpecificOutput.PermissionDecision)
	assert.JSONEq(t, string(ask.Input), string(approve.HookSpecificOutput.UpdatedInput), "an approved plan echoes its input (LIVE-D2)")

	reject := encode(t, hookEventPreToolUse, ask, engine.PermissionAnswer{Message: "split step 2"})
	assert.Equal(t, "deny", reject.HookSpecificOutput.PermissionDecision)
	assert.Equal(t, "split step 2", reject.HookSpecificOutput.PermissionDecisionReason)
	assert.Empty(t, reject.HookSpecificOutput.UpdatedInput)
}

func TestApprovalCodec_EncodeRefuses(t *testing.T) {
	c := codec(t)
	tool := engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}
	for name, tc := range map[string]struct {
		event string
		a     engine.PermissionAnswer
	}{
		"bypass":              {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(modeBypass)}},
		"plan":                {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(modePlan)}},
		"mode on a deny":      {hookEventPermissionRequest, engine.PermissionAnswer{SetMode: engine.Provide(modeDefault)}},
		"rules on a deny":     {hookEventPermissionRequest, engine.PermissionAnswer{SessionRules: []string{"Bash"}}},
		"bad rule":            {hookEventPermissionRequest, engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash("}}},
		"rules on a pretool":  {hookEventPreToolUse, engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash"}}},
		"mode on a pretool":   {hookEventPreToolUse, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(modeDefault)}},
		"unknown event":       {"Stop", engine.PermissionAnswer{Allow: true}},
		"answers not a quest": {hookEventPreToolUse, engine.PermissionAnswer{Allow: true, Answers: []engine.QuestionAnswer{{Question: "q"}}}},
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

func TestApprovalCodec_HostCall(t *testing.T) {
	call, err := codec(t).HostCall(json.RawMessage(liveHostCall))
	require.NoError(t, err)
	assert.Equal(t, engine.HostCall{Tool: "ExitPlanMode", ToolUseID: "toolu_01HKCaGnUQaH15bCRh51MGBp", Input: json.RawMessage(`{"plan":"# Plan","planFilePath":"/p.md"}`)}, call)
	_, err = codec(t).HostCall(json.RawMessage(`{"input":{}}`))
	assert.Error(t, err, "a host call names its tool")
}

func TestApprovalCodec_HostDeny(t *testing.T) {
	s, err := codec(t).HostDeny(`the "hook" did not answer`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"behavior":"deny","message":"the \"hook\" did not answer"}`, s)
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

// TestApprovalCodec_HooksAreClaudesTwoRoutesToTheHuman: claude's approval
// hooks are the permission ask for every tool its posture and rules left
// open (PermissionRequest, no matcher) and the pre-tool hook for exactly the
// two tools only rewritten input can answer (PreToolUse), each running
// `ctxloom hook permission` for its own event and outliving the approval
// timeout.
func TestApprovalCodec_HooksAreClaudesTwoRoutesToTheHuman(t *testing.T) {
	const approval = 15 * time.Minute
	h := approvalCodec{}.Hooks(approval)
	require.Len(t, h.PermissionAsk, 1)
	assert.Equal(t, agent.ApprovalHook("PermissionRequest", "", approval), h.PermissionAsk[0])
	require.Len(t, h.PreTool, 1)
	assert.Equal(t, agent.ApprovalHook("PreToolUse", "AskUserQuestion|ExitPlanMode", approval), h.PreTool[0])
	matcher := regexp.MustCompile("^(?:" + h.PreTool[0].Matcher + ")$")
	for _, tool := range []string{"AskUserQuestion", "ExitPlanMode"} {
		assert.Regexp(t, matcher, tool)
	}
	assert.NotRegexp(t, matcher, "Bash")
	assert.Len(t, h.All(), 2, "no other hook is the approval route's")
}
