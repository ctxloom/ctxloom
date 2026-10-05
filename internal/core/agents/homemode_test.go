package agents

import (
	"strconv"
	"strings"
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

// TestParseHomeMode_UnknownValueIsAnErrorCarryingTheSessionDefault: an
// unrecognized value is an error (every caller refuses on it), and the value
// returned beside it is the SESSION home — what a --degraded launch proceeds
// on. A typo must never land a run on the user's real home, which only an
// explicit "host" selects.
func TestParseHomeMode_UnknownValueIsAnErrorCarryingTheSessionDefault(t *testing.T) {
	// The typo must not CONTAIN a valid value, or the "names the valid values"
	// assertion below is satisfied by the echo of the input alone.
	const typo = "project"
	got, err := ParseHomeMode(typo)
	require.ErrorIs(t, err, ErrUnknownHomeMode, "an unknown engine_home must be reported")
	assert.Contains(t, err.Error(), strconv.Quote(typo), "the error must echo the rejected value")
	assert.Contains(t, err.Error(), strings.Join(HomeModeNames(), ", "), "the error must name the valid values")
	assert.Equal(t, HomeModeSession, got, "beside the error, the degraded default is the session home — never the real home")
}
