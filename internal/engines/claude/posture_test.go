package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// namedPolicies are the postures a golden matrix walks, keyed by the
// names its lines carry: claude's modes, and the two approvers claude
// spells as modes.
var namedPolicies = []struct {
	name string
	p    engine.PermissionPolicy
}{
	{modeDefault, modePolicy(modeDefault)},
	{modePlan, modePolicy(modePlan)},
	{modeBypass, modePolicy(modeBypass)},
	{modeAcceptEdits, modePolicy(modeAcceptEdits)},
	{"dontAsk", withApprover(modePolicy(modeDefault), engine.ApproverNone)},
	{"auto", withApprover(modePolicy(modeDefault), engine.ApproverReviewer)},
}

// withSession projects req's session and gives it p, so a request-driven
// argv runs at p.
func withSession(b *ClaudeCode, req *agent.ExecuteRequest, p engine.PermissionPolicy) *agent.ExecuteRequest {
	s := b.session(req)
	s.Permission = p
	req.Session = &s
	return req
}

// policy is a resolved claude policy over doc: the human approves, no
// sandbox.
func policy(doc map[string]any) engine.PermissionPolicy {
	return engine.PermissionPolicy{Posture: engine.Posture{Engine: EngineName, Document: doc}, Sandbox: engine.SandboxFull}
}

func modePolicy(m string) engine.PermissionPolicy { return policy(map[string]any{keyMode: m}) }

func withApprover(p engine.PermissionPolicy, a engine.Approver) engine.PermissionPolicy {
	p.Approver = a
	return p
}

func planFirst(after string) engine.PermissionPolicy {
	return policy(map[string]any{keyMode: modePlan, keyAfterPlan: after})
}

// headlessPosture decodes p for a headless launch, which must accept it.
func headlessPosture(t *testing.T, p engine.PermissionPolicy) posture {
	t.Helper()
	pos, err := postureOf(p, false)
	require.NoError(t, err)
	return pos
}

// A headless launch carries no --permission-mode: the mode rides each
// turn's --settings, so one mechanism sets it and a later turn can change
// it. Only what never changes per turn is on the launch argv.
func TestPermissionArgs_HeadlessLaunch(t *testing.T) {
	for _, m := range []string{modeDefault, modeAcceptEdits} {
		assert.Equalf(t, hostArgs, permissionArgs(headlessPosture(t, modePolicy(m)), []string{"ctxloom"}), "%s", m)
	}
	assert.Equal(t, []string{flagSkipPermissions, flagPermissionPrompts, "none"},
		permissionArgs(headlessPosture(t, modePolicy(modeBypass)), nil), "bypass stays on the argv, and asks nobody")
	assert.Equal(t, append([]string{flagDisallowedTools, "Bash,Edit,Write,NotebookEdit", flagAllowedTools, "mcp__ctxloom"}, hostArgs...),
		permissionArgs(headlessPosture(t, modePolicy(modePlan)), []string{"ctxloom"}), "plain plan keeps its read-only belt and its MCP grant")
}

// hostArgs hands what the posture and rules leave open to ctxloom's
// permission host, which holds it for the human at the root.
var hostArgs = []string{flagPermissionPromptTool, "mcp__ctxloom__permission_host", flagPermissionPrompts, "host"}

// Plan-first drops the mutating-tool deny list (an approved plan must be
// able to execute; claude's plan mode is read-only on its own) and the
// argv MCP grant, which would outlive the plan turn as blanket permission.
func TestPermissionArgs_PlanFirstCarriesNoReadOnlyBelt(t *testing.T) {
	args := permissionArgs(headlessPosture(t, planFirst(modeAcceptEdits)), []string{"ctxloom"})
	assert.Equal(t, hostArgs, args)
}

// A headless child's human approver is reached through ctxloom's permission
// host; with any other approver nobody is asked, and what the rules leave
// open is denied (claude's own dontAsk or auto decides first).
func TestPermissionArgs_OnlyTheHumanIsReachedThroughTheHost(t *testing.T) {
	for a, want := range map[engine.Approver][]string{
		engine.ApproverHuman:    hostArgs,
		engine.ApproverNone:     {flagPermissionPrompts, "none"},
		engine.ApproverReviewer: {flagPermissionPrompts, "none"},
	} {
		p := modePolicy(modeDefault)
		p.Approver = a
		assert.Equalf(t, want, permissionArgs(headlessPosture(t, p), nil), "%s", a)
	}
}

// claude names two approvers by a mode of its own: none is dontAsk (deny
// what the rules leave open), reviewer is auto (claude's classifier). They
// pair with default; bypass asks nobody, so any approver pairs with it.
// Headless, none pairs with any mode (nothing is asked of anyone there).
func TestClaudeMode_MapsTheApprover(t *testing.T) {
	cases := []struct {
		mode        string
		approver    engine.Approver
		interactive bool
		want        string
	}{
		{modeAcceptEdits, engine.ApproverHuman, true, modeAcceptEdits},
		{modeDefault, engine.ApproverNone, true, "dontAsk"},
		{modeDefault, engine.ApproverReviewer, true, "auto"},
		{modeDefault, engine.ApproverReviewer, false, "auto"},
		{modeDefault, engine.ApproverNone, false, "dontAsk"},
		{modeAcceptEdits, engine.ApproverNone, false, modeAcceptEdits},
		{modePlan, engine.ApproverNone, false, modePlan},
		{modeBypass, engine.ApproverReviewer, true, modeBypass},
		{modeBypass, engine.ApproverNone, true, modeBypass},
	}
	for _, c := range cases {
		got, err := posture{mode: c.mode, approver: c.approver}.claudeMode(c.interactive)
		require.NoErrorf(t, err, "%+v", c)
		assert.Equalf(t, c.want, got, "%+v", c)
	}
}

// A pairing claude has no mode for is refused where the session is built,
// never run as the declared mode with the approver dropped.
func TestPostureOf_RefusesAnApproverClaudeCannotPair(t *testing.T) {
	for _, c := range []struct {
		mode        string
		approver    engine.Approver
		interactive bool
	}{
		{modeAcceptEdits, engine.ApproverNone, true},
		{modePlan, engine.ApproverNone, true},
		{modeAcceptEdits, engine.ApproverReviewer, false},
		{modePlan, engine.ApproverReviewer, true},
	} {
		p := modePolicy(c.mode)
		p.Approver = c.approver
		_, err := postureOf(p, c.interactive)
		assert.ErrorIsf(t, err, ErrPosture, "%+v", c)
		kind, kerr := Build()
		require.NoError(t, kerr)
		m := engine.Structured
		if c.interactive {
			m = engine.Interactive
		}
		_, err = kind.Instance(engine.Session{Mode: m, Permission: p})
		assert.ErrorIsf(t, err, ErrPosture, "Instance refuses it: %+v", c)
	}
}

func TestPostureOf_RefusesAnotherEnginesPosture(t *testing.T) {
	p := modePolicy(modeDefault)
	p.Posture.Engine = "mock"
	_, err := postureOf(p, false)
	assert.ErrorIs(t, err, ErrPosture)
	_, err = postureOf(engine.PermissionPolicy{}, false)
	assert.ErrorIs(t, err, ErrPosture, "an unresolved policy is not a posture")
}

// claude's sandbox covers workspace writes and lets through only named
// hosts; read-only and an open network are not claude can promise, so
// they are refused rather than run wider than declared.
func TestPostureOf_RefusesASandboxClaudeCannotEnforce(t *testing.T) {
	p := modePolicy(modeDefault)
	p.Sandbox = engine.SandboxReadOnly
	_, err := postureOf(p, false)
	assert.ErrorIs(t, err, ErrPosture)
	p.Sandbox, p.Network = engine.SandboxWorkspaceWrite, true
	_, err = postureOf(p, false)
	assert.ErrorIs(t, err, ErrPosture)
	p.Network = false
	_, err = postureOf(p, false)
	assert.NoError(t, err)
}

// workspaceWriteSandbox is the settings' sandbox member for
// workspace-write: it must start or claude exits, with no escape hatch,
// no auto-allow and every unlisted host denied.
var workspaceWriteSandbox = map[string]any{
	"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": false,
	"network": map[string]any{"strictAllowlist": true},
}

func interactiveExec(t *testing.T, p engine.PermissionPolicy, presented ...present.Presentation) (engine.Exec, error) {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	inst, err := kind.Instance(engine.Session{Mode: engine.Interactive, Permission: p, MCPServers: []string{"ctxloom"}})
	require.NoError(t, err)
	return inst.Exec(presented)
}

// settingsDoc is the one --settings value in args, decoded.
func settingsDoc(t *testing.T, args []string) map[string]any {
	t.Helper()
	var at []int
	for i, a := range args {
		if a == flagSettings {
			at = append(at, i)
		}
	}
	require.Len(t, at, 1, "exactly one %s: claude keeps only the last one given (measured, 2.1.285): %v", flagSettings, args)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(args[at[0]+1]), &doc), "the value is inline JSON")
	return doc
}

// The interactive launch is the human's own session: its mode stays on
// --permission-mode, nobody is told to deny prompts, and the declared rules
// ride one inline --settings.
func TestInteractiveExec_ModeFlagAndDeclaredRules(t *testing.T) {
	ex, err := interactiveExec(t, policy(map[string]any{
		keyMode: modeAcceptEdits, keyAllow: []string{"Bash(npm test)"}, keyDeny: []string{"Bash(rm *)"}, keyAsk: []string{"Bash(git push *)"},
	}))
	require.NoError(t, err)
	assert.True(t, argPair(ex.Args, flagPermissionMode, "acceptEdits"))
	assert.NotContains(t, ex.Args, flagPermissionPrompts)
	assert.Equal(t, map[string]any{"permissions": map[string]any{
		"allow": []any{"Bash(npm test)"}, "deny": []any{"Bash(rm *)"}, "ask": []any{"Bash(git push *)"},
	}}, settingsDoc(t, ex.Args))
}

// The human's own session carries the approver's claude mode on the flag.
func TestInteractiveExec_ApproverRidesTheModeFlag(t *testing.T) {
	p := modePolicy(modeDefault)
	p.Approver = engine.ApproverNone
	ex, err := interactiveExec(t, p)
	require.NoError(t, err)
	assert.True(t, argPair(ex.Args, flagPermissionMode, "dontAsk"))
	p.Approver = engine.ApproverReviewer
	ex, err = interactiveExec(t, p)
	require.NoError(t, err)
	assert.True(t, argPair(ex.Args, flagPermissionMode, "auto"))
}

func TestInteractiveExec_SandboxRidesTheSettings(t *testing.T) {
	p := modePolicy(modeDefault)
	p.Sandbox = engine.SandboxWorkspaceWrite
	ex, err := interactiveExec(t, p)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"sandbox": workspaceWriteSandbox}, settingsDoc(t, ex.Args))
}

func TestInteractiveExec_NoRulesNoSettings(t *testing.T) {
	ex, err := interactiveExec(t, modePolicy(modeDefault))
	require.NoError(t, err)
	assert.NotContains(t, ex.Args, flagSettings)
	assert.NotContains(t, ex.Args, flagPermissionMode, "default adds nothing")
}

// A presentation that also names --settings would silently replace the
// rules (claude keeps the last --settings); Exec refuses instead.
func TestInteractiveExec_RefusesASecondSettings(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	_, err := interactiveExec(t, policy(map[string]any{keyMode: modeDefault, keyDeny: []string{"Bash"}}), file)
	assert.ErrorIs(t, err, errSettingsTwice)
}

func headlessTurnArgv(t *testing.T, p engine.PermissionPolicy, in engine.Turn, presented ...present.Presentation) ([]string, error) {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	inst, err := kind.Instance(engine.Session{Mode: engine.Structured, Permission: p, MCPServers: []string{"ctxloom"}})
	require.NoError(t, err)
	ex, err := inst.Exec(presented)
	require.NoError(t, err)
	return (&streamJSONDriver{inst: inst.(*instance)}).argv(ex, in)
}

// Each turn's process carries its posture as its one --settings: the
// turn's mode, the declared rules, and the grants ctxloom holds — which is
// how a posture reaches a process resumed with --resume (a mode does not
// survive a resume).
func TestTurnArgv_CarriesThePostureAsInlineSettings(t *testing.T) {
	p := policy(map[string]any{keyMode: modeAcceptEdits, keyAllow: []string{"Read"}, keyDeny: []string{"Bash(rm *)"}, keyAsk: []string{"WebFetch"}})
	args, err := headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Grants: []string{"Bash(git status)"}}})
	require.NoError(t, err)
	assert.NotContains(t, args, flagPermissionMode)
	assert.Equal(t, map[string]any{"permissions": map[string]any{
		"defaultMode": "acceptEdits",
		"allow":       []any{"Read", "Bash(git status)"},
		"deny":        []any{"Bash(rm *)"},
		"ask":         []any{"WebFetch"},
	}}, settingsDoc(t, args), "a turn naming no mode runs at the launch's")
}

// Deny beats grant: a session grant never displaces a declared deny or ask.
// Both ride the SAME document as the grant, and claude evaluates deny, then
// ask, then allow — so a grant colliding with a deny (or covering it, as a
// whole-tool grant does) still leaves the call denied, and one colliding
// with an ask still sends it to the human.
func TestTurnArgv_AGrantNeverDisplacesADeclaredDenyOrAsk(t *testing.T) {
	p := policy(map[string]any{keyMode: modeDefault, keyDeny: []string{"Bash(rm *)"}, keyAsk: []string{"WebFetch"}})
	args, err := headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Grants: []string{"Bash(rm *)", "Bash", "WebFetch"}}})
	require.NoError(t, err)
	perms, ok := settingsDoc(t, args)["permissions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"Bash(rm *)"}, perms["deny"])
	assert.Equal(t, []any{"WebFetch"}, perms["ask"])
	assert.Equal(t, []any{"Bash(rm *)", "Bash", "WebFetch"}, perms["allow"])
}

// The reviewer rides the turn as claude's auto; the sandbox rides beside
// the rules.
func TestTurnArgv_ApproverAndSandbox(t *testing.T) {
	p := modePolicy(modeDefault)
	p.Approver, p.Sandbox = engine.ApproverReviewer, engine.SandboxWorkspaceWrite
	args, err := headlessTurnArgv(t, p, engine.Turn{})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"permissions": map[string]any{"defaultMode": "auto"},
		"sandbox":     workspaceWriteSandbox,
	}, settingsDoc(t, args))
}

// The turn's own mode wins over the launch's: a plan-first child starts in
// plan and continues at its after-plan posture, and the MCP grant plan
// needs rides only the plan turns.
func TestTurnArgv_PlanFirstModeFollowsTheTurn(t *testing.T) {
	p := planFirst(modeAcceptEdits)
	args, err := headlessTurnArgv(t, p, engine.Turn{})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"permissions": map[string]any{"defaultMode": "plan", "allow": []any{"mcp__ctxloom"}}}, settingsDoc(t, args))

	args, err = headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Mode: modeAcceptEdits}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"permissions": map[string]any{"defaultMode": "acceptEdits"}}, settingsDoc(t, args))
}

func TestTurnArgv_NothingToSayNoSettings(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeBypass), engine.Turn{})
	require.NoError(t, err)
	assert.NotContains(t, args, flagSettings)
	assert.Contains(t, args, flagSkipPermissions)
}

// Bypass never rides a turn's settings, even if a posture names it.
func TestTurnArgv_NeverCarriesBypass(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeBypass), engine.Turn{Posture: engine.TurnPosture{Mode: modeBypass}})
	require.NoError(t, err)
	assert.NotContains(t, args, flagSettings)
}

func TestTurnArgv_RefusesASecondSettings(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	_, err := headlessTurnArgv(t, policy(map[string]any{keyMode: modeDefault, keyDeny: []string{"Bash"}}), engine.Turn{}, file)
	assert.ErrorIs(t, err, errSettingsTwice)
}
