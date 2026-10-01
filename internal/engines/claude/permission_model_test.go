package claude

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func model(t *testing.T) engine.PermissionModel {
	t.Helper()
	m, ok := Claude{}.Permissions().Get()
	require.True(t, ok, "claude declares its permission model")
	return m
}

func declared(docs ...map[string]any) []engine.Declaration {
	var out []engine.Declaration
	for i, d := range docs {
		out = append(out, engine.Declaration{Document: d, From: fmt.Sprintf("rung %d", i)})
	}
	return out
}

func TestPermissionModel_Vocabulary(t *testing.T) {
	m := model(t)
	assert.Equal(t, []string{"mode", "after_plan", "allow", "deny", "ask"}, m.Keys())
	assert.Equal(t, []string{"default", "acceptEdits", "plan", "bypass"}, m.Postures(),
		"dontAsk and auto are not claude modes here: they are approver none and approver reviewer")
	assert.True(t, m.Reviewer(), "claude's auto classifier serves approver: reviewer")
	assert.Equal(t, engine.SandboxFull, m.DefaultSandbox(), "undeclared: today's behaviour")
}

func TestPermissionModel_Validate(t *testing.T) {
	m := model(t)
	assert.NoError(t, m.Validate(map[string]any{"mode": "plan", "after_plan": "acceptEdits", "deny": []any{"Bash(rm *)"}}))
	for name, doc := range map[string]map[string]any{
		"unknown key":         {"mdoe": "plan"},
		"dontAsk":             {"mode": "dontAsk"},
		"auto":                {"mode": "auto"},
		"after_plan bypass":   {"mode": "plan", "after_plan": "bypass"},
		"after_plan off plan": {"mode": "default", "after_plan": "acceptEdits"},
		"bad rule":            {"allow": []any{"Bash("}},
		"rules not a list":    {"deny": "Bash"},
		"mode not a string":   {"mode": 3},
	} {
		assert.Errorf(t, m.Validate(doc), "%s", name)
	}
	err := m.Validate(map[string]any{"mode": "dontAsk"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver: none", "the refusal names what replaced it")
	err = m.Validate(map[string]any{"mode": "auto"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver: reviewer")
}

// Each key comes from the nearest rung that declares it; nothing declared
// is the host default, acceptEdits.
func TestPermissionModel_ResolveFieldByField(t *testing.T) {
	m := model(t)
	doc, err := m.Resolve(engine.PostureRequest{Declared: declared(
		map[string]any{"allow": []any{"Read"}},
		map[string]any{"mode": "plan", "after_plan": "acceptEdits", "deny": []any{"Bash(rm *)"}, "allow": []any{"Glob"}},
	)})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "plan", "after_plan": "acceptEdits", "allow": []string{"Read"}, "deny": []string{"Bash(rm *)"}}, doc)

	doc, err = m.Resolve(engine.PostureRequest{})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "acceptEdits"}, doc)
}

func TestPermissionModel_FlagOverridesTheMode(t *testing.T) {
	m := model(t)
	doc, err := m.Resolve(engine.PostureRequest{Mode: "bypass", Declared: declared(map[string]any{"mode": "plan", "after_plan": "default", "deny": []any{"Bash"}})})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "bypass", "deny": []string{"Bash"}}, doc, "a flag off plan leaves nothing for after_plan to continue")
	_, err = m.Resolve(engine.PostureRequest{Mode: "yolo"})
	assert.Error(t, err)
}

func TestPermissionModel_DegradedFallsToTheFloor(t *testing.T) {
	m := model(t)
	_, err := m.Resolve(engine.PostureRequest{Declared: declared(map[string]any{"mode": "plann"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"plann"`)
	assert.Contains(t, err.Error(), "rung 0")

	var warned []string
	doc, err := m.Resolve(engine.PostureRequest{Degraded: true, Warn: func(f string, a ...any) { warned = append(warned, fmt.Sprintf(f, a...)) }, Declared: declared(map[string]any{"mode": "plann"})})
	require.NoError(t, err)
	assert.Equal(t, m.Floor(), doc)
	assert.Equal(t, map[string]any{"mode": "plan"}, m.Floor())
	require.Len(t, warned, 1, "the drop is announced")
}

func TestPermissionModel_DecodeAndTransitions(t *testing.T) {
	m := model(t)
	name, err := m.Decode(map[string]any{"mode": "plan", "after_plan": "acceptEdits"})
	require.NoError(t, err)
	assert.Equal(t, "plan", name)
	_, err = m.Decode(map[string]any{"mode": "sideways"})
	assert.Error(t, err)
	assert.Equal(t, []string{"default", "acceptEdits"}, m.Transitions(map[string]any{"mode": "plan"}))
	assert.Empty(t, m.Transitions(map[string]any{"mode": "bypass"}), "nothing an approval changes on a bypass session")
	assert.Equal(t, []string{"acceptEdits", "default"}, m.Transitions(map[string]any{"mode": "plan", "after_plan": "acceptEdits"}),
		"a plan-first session's declared continuation comes first: the presenter's default selection")
	assert.Equal(t, []string{"default", "acceptEdits"}, m.Transitions(map[string]any{"mode": "plan", "after_plan": "default"}))
}

// Fail closed: only what claude can be made to enforce (with
// failIfUnavailable) is offered; read-only needs a filesystem deny over the
// working tree that is not verified, so it is not offered anywhere.
func TestPermissionModel_Sandboxes(t *testing.T) {
	m := model(t)
	want := []engine.Sandbox{engine.SandboxFull}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		want = []engine.Sandbox{engine.SandboxWorkspaceWrite, engine.SandboxFull}
	}
	assert.Equal(t, want, m.Sandboxes("host"))
	assert.Equal(t, []engine.Sandbox{engine.SandboxFull}, m.Sandboxes("container-rootless"), "an engine sandbox inside a container is unverified")
	assert.NotContains(t, m.Sandboxes("host"), engine.SandboxReadOnly)
}
