package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func TestMockPermissionModel(t *testing.T) {
	m, ok := New().Permissions().Get()
	require.True(t, ok)
	assert.Equal(t, []string{"default", "plan", "bypass"}, m.Postures())
	assert.Equal(t, []string{"mode", "allow", "deny", "ask"}, m.Keys())
	assert.False(t, m.Reviewer())
	assert.Equal(t, []engine.Sandbox{engine.SandboxFull}, m.Sandboxes("host"))
	assert.Equal(t, engine.SandboxFull, m.DefaultSandbox())

	assert.Error(t, m.Validate(map[string]any{"mode": "acceptEdits"}), "not a mock mode")
	assert.Error(t, m.Validate(map[string]any{"after_plan": "default"}), "the mock has no plan continuation")
	assert.Error(t, m.Validate(map[string]any{"deny": []any{" "}}))

	doc, err := m.Resolve(engine.PostureRequest{Declared: []engine.Declaration{{Document: map[string]any{"deny": []any{"Bash"}}}, {Document: map[string]any{"mode": "plan", "deny": []any{"Edit"}}}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "plan", "deny": []string{"Bash"}}, doc)
	doc, err = m.Resolve(engine.PostureRequest{})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "default"}, doc)
	doc, err = m.Resolve(engine.PostureRequest{Degraded: true, Declared: []engine.Declaration{{Document: map[string]any{"mode": "nope"}}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "plan"}, doc)
	name, err := m.Decode(map[string]any{"mode": "bypass"})
	require.NoError(t, err)
	assert.Equal(t, "bypass", name)
	assert.Empty(t, m.Transitions(doc))
}
