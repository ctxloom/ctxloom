package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGet_UnknownEngineErrorNamesTheValidSet pins the one thing a rejected
// --engine value has to tell the user. This package's whole design premise is
// that a typo must be a hard error rather than a silent prefix match (Get's
// own doc comment says so), which is only actionable if the refusal also says
// what WOULD have been accepted: a user who typed "claud" is told nothing by
// `unknown engine "claud"` alone. The error must name every engine the
// registry holds, derived from the registry itself so a newly registered
// engine cannot be omitted from the message.
func TestGet_UnknownEngineErrorNamesTheValidSet(t *testing.T) {
	_, err := Get("claud")
	require.Error(t, err)
	msg := err.Error()

	assert.Contains(t, msg, `"claud"`, "the rejected input must still be named")
	for _, e := range All() {
		assert.Contains(t, msg, e.Name(), "every registered engine must appear in the refusal")
	}
}
