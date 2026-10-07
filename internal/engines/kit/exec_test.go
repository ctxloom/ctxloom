package kit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestComposeEnv_HomeVarsThenPresentationsInOrder: each home var at its
// bound path, then each presentation's env in delivery order; a later value
// overrides an earlier one, a home var included.
func TestComposeEnv_HomeVarsThenPresentationsInOrder(t *testing.T) {
	s := engine.Session{Home: []engine.HomeBinding{{Var: "H", Path: "/h"}, {Var: "X", Path: "/x"}}}
	env := ComposeEnv(s, []present.Presentation{
		{Env: map[string]string{"A": "1", "H": "/surface"}},
		{Env: map[string]string{"A": "2"}},
	})
	assert.Equal(t, map[string]string{"H": "/surface", "X": "/x", "A": "2"}, env)
	assert.Equal(t, map[string]string{}, ComposeEnv(engine.Session{}, nil), "never nil")
}

// TestPresentedArgs_InDeliveryOrderNeverNil: every presentation's args in
// order; none is an empty, non-nil argv.
func TestPresentedArgs_InDeliveryOrderNeverNil(t *testing.T) {
	args, err := PresentedArgs([]present.Presentation{{Args: []string{"-a", "1"}}, {}, {Args: []string{"-b"}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"-a", "1", "-b"}, args)
	args, err = PresentedArgs(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{}, args)
}

// TestPresentedArgs_RefuseVetoes: refuse is asked about each presentation;
// the first refusal is the error, as is.
func TestPresentedArgs_RefuseVetoes(t *testing.T) {
	no := errors.New("no")
	var asked []string
	refuse := func(p present.Presentation) error {
		asked = append(asked, p.EnginePath)
		if p.EnginePath == "bad" {
			return no
		}
		return nil
	}
	_, err := PresentedArgs([]present.Presentation{{EnginePath: "ok", Args: []string{"-a"}}, {EnginePath: "bad"}, {EnginePath: "after"}}, refuse)
	require.ErrorIs(t, err, no)
	assert.Equal(t, []string{"ok", "bad"}, asked)
}
