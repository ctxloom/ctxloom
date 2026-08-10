package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGet_UnknownEngineErrorNamesTheValidSet pins the one thing a rejected
// --engine value has to tell the user. This package's whole design premise is
// that a typo must be a hard error rather than a silent prefix match (Get's
// own doc comment says so), which is only actionable if the refusal also says
// what WOULD have been accepted: a user who typed "claud" is told nothing by
// `unknown engine "claud"` alone, and the accepted spellings live in two
// places (this registry and agent.CanonicalEngineName's alias table) that no
// user can read. The error must name every canonical engine the registry
// holds, derived from the registry itself so a newly registered engine cannot
// be omitted from the message.
func TestGet_UnknownEngineErrorNamesTheValidSet(t *testing.T) {
	_, err := Get("claud")
	require.Error(t, err)
	msg := err.Error()

	assert.Contains(t, msg, `"claud"`, "the rejected input must still be named")
	for _, e := range All() {
		assert.Contains(t, msg, e.Name(), "every registered engine must appear in the refusal")
	}
}

// TestGet_UnknownEngineErrorNamesAcceptedAliases pins that the refusal also
// covers the alias spellings, since "claude" is an accepted value a
// user may be trying to recall and it is not any engine's Name().
func TestGet_UnknownEngineErrorNamesAcceptedAliases(t *testing.T) {
	_, err := Get("agyy")
	require.Error(t, err)
	msg := err.Error()

	for _, alias := range []string{"claude", "claudecode", "agy"} {
		assert.True(t, strings.Contains(msg, alias),
			"accepted alias %q must appear in the refusal, got: %s", alias, msg)
	}
}
