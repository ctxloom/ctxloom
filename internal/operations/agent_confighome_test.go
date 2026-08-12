package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
)

// TestResolveConfigHome_UndeclaredDefaultsToHost pins the default direction
// directly: MUTATION TARGET m1 — flip it to agents.ConfigHomeProject and this
// goes red.
func TestResolveConfigHome_UndeclaredDefaultsToHost(t *testing.T) {
	got, err := ResolveConfigHome("")
	require.NoError(t, err)
	assert.Equal(t, agents.ConfigHomeHost, got)
}

// TestResolveConfigHome_AcceptsBothDeclaredValues proves "project" and "host"
// both round-trip unchanged and with no error — the opt-in and the explicit
// opt-out are equally valid declarations.
func TestResolveConfigHome_AcceptsBothDeclaredValues(t *testing.T) {
	for _, want := range []string{agents.ConfigHomeProject, agents.ConfigHomeHost} {
		got, err := ResolveConfigHome(want)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// TestResolveConfigHome_UnknownValueWarnsAndDefaultsToHost is the RESOLVE-time
// (as opposed to write-time) treatment: an unresolvable value returns an error
// for the CALLER to warn with, but the returned value is still the safe
// default (host) so a hand-edited config.yaml never blocks a launch over this.
func TestResolveConfigHome_UnknownValueWarnsAndDefaultsToHost(t *testing.T) {
	got, err := ResolveConfigHome("projectt")
	require.Error(t, err, "an unknown config_home must be reported")
	assert.Contains(t, err.Error(), "projectt")
	assert.Contains(t, err.Error(), "project", "the error must name the valid values")
	assert.Contains(t, err.Error(), "host", "the error must name the valid values")
	assert.Equal(t, agents.ConfigHomeHost, got, "even on error, the safe default is returned")
}
