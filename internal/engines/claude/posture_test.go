package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

func planFirst(after engine.PermissionMode) engine.PermissionPolicy {
	return engine.PermissionPolicy{Mode: engine.PermissionPlan, AfterPlan: engine.Provide(after), Ceiling: after}
}

// A headless launch carries no --permission-mode: the mode rides each
// turn's --settings, so one mechanism sets it and a later turn can change
// it. Only what never changes per turn is on the launch argv.
func TestPermissionArgs_HeadlessLaunch(t *testing.T) {
	for _, m := range []engine.PermissionMode{engine.PermissionDefault, engine.PermissionAcceptEdits, engine.PermissionDontAsk, engine.PermissionAuto} {
		assert.Equalf(t, []string{flagPermissionPrompts, "none"}, permissionArgs(engine.PermissionPolicy{Mode: m}, []string{"ctxloom"}), "%s", m)
	}
	assert.Equal(t, []string{flagSkipPermissions, flagPermissionPrompts, "none"},
		permissionArgs(engine.PermissionPolicy{Mode: engine.PermissionBypass}, nil), "bypass stays on the argv")
	assert.Equal(t, []string{flagDisallowedTools, "Bash,Edit,Write,NotebookEdit", flagAllowedTools, "mcp__ctxloom", flagPermissionPrompts, "none"},
		permissionArgs(engine.PermissionPolicy{Mode: engine.PermissionPlan}, []string{"ctxloom"}), "plain plan keeps its read-only belt and its MCP grant")
}

// Plan-first drops the mutating-tool deny list (an approved plan must be
// able to execute; claude's plan mode is read-only on its own) and the
// argv MCP grant, which would outlive the plan turn as blanket permission.
func TestPermissionArgs_PlanFirstCarriesNoReadOnlyBelt(t *testing.T) {
	args := permissionArgs(planFirst(engine.PermissionAcceptEdits), []string{"ctxloom"})
	assert.Equal(t, []string{flagPermissionPrompts, "none"}, args)
}

// Until ctxloom serves the permission host, a headless child's approver
// cannot be reached, whoever it is: what the rules leave open is denied.
func TestPermissionArgs_EveryApproverDeniesWhatIsLeftOpen(t *testing.T) {
	for _, a := range []engine.Approver{engine.ApproverHuman, engine.ApproverNone} {
		args := permissionArgs(engine.PermissionPolicy{Mode: engine.PermissionDefault, Approver: a}, nil)
		assert.Equalf(t, []string{flagPermissionPrompts, "none"}, args, "%s", a)
		assert.NotContainsf(t, args, "--permission-prompt-tool", "%s", a)
	}
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
	ex, err := interactiveExec(t, engine.PermissionPolicy{
		Mode: engine.PermissionAcceptEdits, Allow: []string{"Bash(npm test)"}, Deny: []string{"Bash(rm *)"}, Ask: []string{"Bash(git push *)"},
	})
	require.NoError(t, err)
	assert.True(t, argPair(ex.Args, flagPermissionMode, "acceptEdits"))
	assert.NotContains(t, ex.Args, flagPermissionPrompts)
	assert.Equal(t, map[string]any{"permissions": map[string]any{
		"allow": []any{"Bash(npm test)"}, "deny": []any{"Bash(rm *)"}, "ask": []any{"Bash(git push *)"},
	}}, settingsDoc(t, ex.Args))
}

func TestInteractiveExec_NoRulesNoSettings(t *testing.T) {
	ex, err := interactiveExec(t, engine.PermissionPolicy{Mode: engine.PermissionDefault})
	require.NoError(t, err)
	assert.NotContains(t, ex.Args, flagSettings)
}

// A presentation that also names --settings would silently replace the
// rules (claude keeps the last --settings); Exec refuses instead.
func TestInteractiveExec_RefusesASecondSettings(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	_, err := interactiveExec(t, engine.PermissionPolicy{Mode: engine.PermissionDefault, Deny: []string{"Bash"}}, file)
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
	p := engine.PermissionPolicy{Mode: engine.PermissionAcceptEdits, Allow: []string{"Read"}, Deny: []string{"Bash(rm *)"}, Ask: []string{"WebFetch"}}
	args, err := headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Mode: engine.PermissionAcceptEdits, Grants: []string{"Bash(git status)"}}})
	require.NoError(t, err)
	assert.NotContains(t, args, flagPermissionMode)
	assert.Equal(t, map[string]any{"permissions": map[string]any{
		"defaultMode": "acceptEdits",
		"allow":       []any{"Read", "Bash(git status)"},
		"deny":        []any{"Bash(rm *)"},
		"ask":         []any{"WebFetch"},
	}}, settingsDoc(t, args))
}

// The turn's own mode wins over the launch's: a plan-first child starts in
// plan and continues at its after-plan posture, and the MCP grant plan
// needs rides only the plan turns.
func TestTurnArgv_PlanFirstModeFollowsTheTurn(t *testing.T) {
	p := planFirst(engine.PermissionAcceptEdits)
	args, err := headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Mode: engine.PermissionPlan}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"permissions": map[string]any{"defaultMode": "plan", "allow": []any{"mcp__ctxloom"}}}, settingsDoc(t, args))

	args, err = headlessTurnArgv(t, p, engine.Turn{Posture: engine.TurnPosture{Mode: engine.PermissionAcceptEdits}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"permissions": map[string]any{"defaultMode": "acceptEdits"}}, settingsDoc(t, args))
}

func TestTurnArgv_NothingToSayNoSettings(t *testing.T) {
	args, err := headlessTurnArgv(t, engine.PermissionPolicy{Mode: engine.PermissionBypass}, engine.Turn{})
	require.NoError(t, err)
	assert.NotContains(t, args, flagSettings)
	assert.Contains(t, args, flagSkipPermissions)
}

// Bypass never rides a turn's settings, even if a posture names it.
func TestTurnArgv_NeverCarriesBypass(t *testing.T) {
	args, err := headlessTurnArgv(t, engine.PermissionPolicy{Mode: engine.PermissionBypass}, engine.Turn{Posture: engine.TurnPosture{Mode: engine.PermissionBypass}})
	require.NoError(t, err)
	assert.NotContains(t, args, flagSettings)
}

func TestTurnArgv_RefusesASecondSettings(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	_, err := headlessTurnArgv(t, engine.PermissionPolicy{Mode: engine.PermissionDefault}, engine.Turn{Posture: engine.TurnPosture{Mode: engine.PermissionDefault}}, file)
	assert.ErrorIs(t, err, errSettingsTwice)
}
