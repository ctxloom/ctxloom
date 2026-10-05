package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

func envWith(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

// TestAgentTokenMissing_IsTheEnginesOwnRefusal: init, auth and run all ask
// the one question — is the token every agent authenticates with exported —
// and answer it with the engine's own refusal, so the fix the human reads is
// the engine's one wording wherever they meet it.
func TestAgentTokenMissing_IsTheEnginesOwnRefusal(t *testing.T) {
	reg := engines.Registry()
	backend := claude.EngineName

	err := AgentTokenMissing(reg, backend, envWith(nil))
	require.ErrorIs(t, err, engine.ErrNoCredential)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the refusal names its fix")

	kind, _ := reg.Lookup(engine.Name(backend))
	a, _ := kind.Home().Auth.Get()
	_, authErr := a.Credentials(engine.AuthToken, envWith(nil))
	authFix, _ := clifmt.RemedyOf(authErr)
	assert.Equal(t, authFix, fix, "the same wording `ctxloom auth` shows")

	assert.NoError(t, AgentTokenMissing(reg, backend, envWith(map[string]string{claude.OAuthTokenEnv: "x"})))
	assert.NoError(t, AgentTokenMissing(reg, "no-such-engine", envWith(nil)), "an unknown engine declares no token")
}
