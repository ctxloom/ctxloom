package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseHomeMode_UndeclaredDefaultsToHost pins the default direction
// directly: MUTATION TARGET m1 — flip it to HomeModeSession and this
// goes red.
func TestParseHomeMode_UndeclaredDefaultsToHost(t *testing.T) {
	got, err := ParseHomeMode("")
	require.NoError(t, err)
	assert.Equal(t, HomeModeHost, got)
}

// TestParseHomeMode_AcceptsBothDeclaredValues proves "session" and "host"
// both round-trip unchanged and with no error — the opt-in and the explicit
// opt-out are equally valid declarations.
func TestParseHomeMode_AcceptsBothDeclaredValues(t *testing.T) {
	for _, want := range []HomeMode{HomeModeSession, HomeModeHost} {
		got, err := ParseHomeMode(string(want))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// TestParseHomeMode_UnknownValueWarnsAndDefaultsToHost is the RESOLVE-time
// (as opposed to write-time) treatment: an unresolvable value returns an error
// for the CALLER to warn with, but the returned value is still the safe
// default (host) so a hand-edited config.yaml never blocks a launch over this.
func TestParseHomeMode_UnknownValueWarnsAndDefaultsToHost(t *testing.T) {
	got, err := ParseHomeMode("sessionn")
	require.Error(t, err, "an unknown engine_home must be reported")
	assert.Contains(t, err.Error(), "sessionn")
	assert.Contains(t, err.Error(), "session", "the error must name the valid values")
	assert.Contains(t, err.Error(), "host", "the error must name the valid values")
	assert.Equal(t, HomeModeHost, got, "even on error, the safe default is returned")
}
