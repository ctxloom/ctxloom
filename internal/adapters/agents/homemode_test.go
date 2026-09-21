package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseHomeMode_UndeclaredDefaultsToSession pins the default direction
// (ruled 2026-09-21: the session home by default; the real home only by
// the binding's unsafe selection). MUTATION TARGET m1 — flip it to
// HomeModeHost and this goes red.
func TestParseHomeMode_UndeclaredDefaultsToSession(t *testing.T) {
	got, err := ParseHomeMode("")
	require.NoError(t, err)
	assert.Equal(t, HomeModeSession, got)
}

// TestParseHomeMode_AcceptsBothDeclaredValues proves "session" and "host"
// both round-trip unchanged and with no error — the default restated and
// the unsafe selection are equally valid declarations.
func TestParseHomeMode_AcceptsBothDeclaredValues(t *testing.T) {
	for _, want := range []HomeMode{HomeModeSession, HomeModeHost} {
		got, err := ParseHomeMode(string(want))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// TestParseHomeMode_UnknownValueWarnsAndDefaultsToSession is the RESOLVE-time
// (as opposed to write-time) treatment: an unresolvable value returns an error
// for the CALLER to warn with, but the returned value is still the safe
// default so a hand-edited config.yaml never blocks a launch over this — and
// the safe default is the SESSION home: a typo must never land a run on the
// user's real home, which only an explicit "host" selects.
func TestParseHomeMode_UnknownValueWarnsAndDefaultsToSession(t *testing.T) {
	// The typo must not CONTAIN a valid value, or the "names the valid values"
	// assertion below is satisfied by the echo of the input alone.
	got, err := ParseHomeMode("project")
	require.Error(t, err, "an unknown engine_home must be reported")
	assert.Contains(t, err.Error(), `"project"`, "the error must echo the rejected value")
	assert.Contains(t, err.Error(), "known: host, session", "the error must name the valid values")
	assert.Equal(t, HomeModeSession, got, "even on error, the safe default is returned — never the real home")
}
