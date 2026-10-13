package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// mock implements the tool-less marker (marauding-magnolia, decision #5):
// the compile-time half of the proof.
var _ engine.ToolLess = Mock{}

// The mock has no tools — its "tools" are prompt markers the echo scripts —
// so its tool-less launch is its ordinary one: the same Exec, and the same
// requiredness refusal.
func TestToolLess_IsTheOrdinaryLaunch(t *testing.T) {
	m := New().(Mock)
	s := engine.Session{Mode: engine.Structured, WorkDir: "/work", Home: []engine.HomeBinding{{Var: "MOCK_HOME", Path: "/h"}}}
	presented := []present.Presentation{{Args: []string{"--context", "/h/ctx.md"}, Env: map[string]string{"A": "1"}}}

	ordinary, err := m.Instance(s)
	require.NoError(t, err)
	want, err := ordinary.Exec(presented)
	require.NoError(t, err)

	toolLess, err := m.ToolLess(s)
	require.NoError(t, err)
	got, err := toolLess.Exec(presented)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, engine.Exec{Binary: "mock", Args: []string{"--context", "/h/ctx.md"}, Env: map[string]string{"MOCK_HOME": "/h", "A": "1"}, WorkDir: "/work"}, got)

	_, err = New(Without(present.Context)).(Mock).ToolLess(s)
	var unsupported engine.ErrUnsupported
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "context", unsupported.Capability)
}
