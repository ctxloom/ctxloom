package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseConfigHome_UndeclaredDefaultsToProject pins the default direction
// directly: a binding that never mentions config_home gets the controlled,
// per-session home, and sharing the human's real engine home is reachable
// only by declaring it. MUTATION TARGET m1 — flip the "" arm to
// ConfigHomeHost and this goes red.
func TestParseConfigHome_UndeclaredDefaultsToProject(t *testing.T) {
	got, err := ParseConfigHome("")
	require.NoError(t, err)
	assert.Equal(t, ConfigHomeProject, got)
	assert.NotEqual(t, ConfigHomeHost, got, "an undeclared config_home must never share the real host home")
}

// TestParseConfigHome_AcceptsBothDeclaredValues proves "project" and "host"
// both round-trip unchanged and with no error — the opt-in and the explicit
// opt-out are equally valid declarations.
func TestParseConfigHome_AcceptsBothDeclaredValues(t *testing.T) {
	for _, want := range []ConfigHome{ConfigHomeProject, ConfigHomeHost} {
		got, err := ParseConfigHome(string(want))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// TestParseConfigHome_UnknownValueWarnsAndDefaultsToProject is the
// RESOLVE-time (as opposed to write-time) treatment: an unresolvable value
// returns an error for the CALLER to warn with, and still does not block the
// launch — but it degrades to the PRIVATE home. A typo is the case where the
// user most clearly meant something, so reading it as consent to share the
// real host home would be a silent share on a mistake.
//
// MUTATION TARGET — return ConfigHomeHost from the default arm and this goes
// red.
func TestParseConfigHome_UnknownValueWarnsAndDefaultsToProject(t *testing.T) {
	got, err := ParseConfigHome("projectt")
	require.Error(t, err, "an unknown config_home must be reported")
	assert.Contains(t, err.Error(), "projectt")
	assert.Contains(t, err.Error(), "project", "the error must name the valid values")
	assert.Contains(t, err.Error(), "host", "the error must name the valid values")
	assert.Equal(t, ConfigHomeProject, got, "even on error, the private home is returned")
	assert.NotEqual(t, ConfigHomeHost, got, "a typo must never degrade into sharing the real host home")
}
