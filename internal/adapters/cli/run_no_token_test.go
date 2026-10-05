package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestRun_NoToken_RefusedBeforeLaunchWithTheEnginesFix: a token-mode run
// (the default `auth:`) with no token exported is refused BEFORE anything
// launches or is written, with the engine's own fix — the wording init and
// auth show — and the refusal status.
func TestRun_NoToken_RefusedBeforeLaunchWithTheEnginesFix(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--one-shot", "-p", "dev", "hi"},
	} {
		t.Run(args[1], func(t *testing.T) {
			runCLIFixture(t)
			t.Setenv(claude.OAuthTokenEnv, "")
			resetApp()

			err := runCLI(t, args...).err
			require.ErrorIs(t, err, engine.ErrNoCredential)
			fix, ok := clifmt.RemedyOf(err)
			require.True(t, ok)
			want, _ := clifmt.RemedyOf(operations.AgentTokenMissing(App().Engines(), claude.EngineName, func(string) (string, bool) { return "", false }))
			assert.Equal(t, want, fix, "the engine's wording, as init and auth show it")
			assert.Equal(t, exitCodeRefused, errorExitCode(err))
		})
	}
}

// TestErrorExitCode_OrdinaryErrorsAreOne: only a refusal carries the
// refusal status; every other ctxloom error stays exitCodeError.
func TestErrorExitCode_OrdinaryErrorsAreOne(t *testing.T) {
	assert.Equal(t, exitCodeError, errorExitCode(assert.AnError))
}
