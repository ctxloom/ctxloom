package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestExec_ComposesHomeVarsThenPresentationsInDeliveryOrder pins the mock's
// whole Exec: binary "mock" whatever the label says, every presentation's
// args in delivery order, and the env as each home var at its bound path then
// each presentation's env in order (a later one overriding an earlier one and
// a home var).
func TestExec_ComposesHomeVarsThenPresentationsInDeliveryOrder(t *testing.T) {
	m := New().(Mock)
	s := engine.Session{
		Mode:    engine.Structured,
		WorkDir: "/work",
		Label:   engine.LabelConfig{Binary: "/opt/other"},
		Home:    []engine.HomeBinding{{Var: "MOCK_HOME", Path: "/h"}, {Var: "XDG_X", Path: "/h/x"}},
	}
	inst, err := m.Instance(s)
	require.NoError(t, err)
	ex, err := inst.Exec([]present.Presentation{
		{Args: []string{"--a", "1"}, Env: map[string]string{"A": "1", "MOCK_HOME": "/from-surface"}},
		{Args: []string{"--b"}, Env: map[string]string{"A": "2"}},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.Exec{
		Binary:  "mock",
		Args:    []string{"--a", "1", "--b"},
		Env:     map[string]string{"MOCK_HOME": "/from-surface", "XDG_X": "/h/x", "A": "2"},
		WorkDir: "/work",
	}, ex)

	ex, err = inst.Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{}, ex.Args, "no presentation: an empty, non-nil argv")
	assert.Equal(t, map[string]string{"MOCK_HOME": "/h", "XDG_X": "/h/x"}, ex.Env)
}
