package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestCredentialRefusedOutcome_ExitsRefusedNotTheEnginesStatus: when the
// engine refused the session's credential after launch, the engine itself
// often exits 0 ("Not logged in" printed, nothing done). The run did none of
// what was asked, so ctxloom exits with the refusal status — the same one a
// credential refused BEFORE launch gets — and the notice already printed
// stands as its message.
func TestCredentialRefusedOutcome_ExitsRefusedNotTheEnginesStatus(t *testing.T) {
	refused := []coord.CredentialHold{{Kind: agent.FailureCredentialRejected, Harps: []string{"other", "mine"}}}

	err := credentialRefusedOutcome(nil, refused, "mine")
	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, exitCodeRefused, exit.Code)

	assert.NoError(t, credentialRefusedOutcome(nil, refused, "someone-else"), "another session's refusal is not this run's")
	limited := []coord.CredentialHold{{Kind: agent.FailureRateLimited, Harps: []string{"mine"}}}
	assert.NoError(t, credentialRefusedOutcome(nil, limited, "mine"), "a rate limit is not a refusal")
	engineFailed := &ExitError{Code: 7}
	assert.Equal(t, engineFailed, credentialRefusedOutcome(engineFailed, nil, "mine"), "no refusal: the run's own outcome stands")
}
