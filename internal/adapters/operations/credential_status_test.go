package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// claudeCredentialProject is a scaffolded claude-code project whose config
// declares mode as the human's session auth, under a scratch HOME that holds
// a native login (signedIn) or not. No credential variable is exported.
func claudeCredentialProject(t *testing.T, mode engine.AuthMode, signedIn bool) *config.Config {
	t.Helper()
	_, cfg := setupProject(t, claude.EngineName)
	fixture := cfg.ToFixture()
	fixture.Auth = mode
	token := ""
	if signedIn {
		token = tokenFixture
	}
	fakeHostHome(t, token)
	return config.NewFixture(fixture)
}

// TestCredentialStatus_RunDoctorAndInitAnswerAlike pins the one resolver: with
// the token unset, a run's preview, doctor's row and init's question all
// carry the SAME refusal and fix; with it set, all are clean. Reading the
// credential three ways is how doctor said ok on a project every run refused.
func TestCredentialStatus_RunDoctorAndInitAnswerAlike(t *testing.T) {
	reg := engines.Registry()
	for _, tc := range []struct {
		name    string
		env     map[string]string
		missing bool
	}{
		{"token unset", nil, true},
		{"token set", map[string]string{claude.OAuthTokenEnv: tokenFixture}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := claudeCredentialProject(t, engine.AuthToken, false)
			lookup := envWith(tc.env)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			row := doctorCheckCredentials(reg, cfg, lookup)
			init := AgentCredentialMissing(reg, claude.EngineName, engine.AuthToken, lookup)
			_, run := previewRunAuth(reg, runAuth{Backend: claude.EngineName, Mode: engine.AuthToken})

			if !tc.missing {
				assert.Equal(t, DoctorOK, row.Status, row.Detail)
				assert.NoError(t, init)
				assert.NoError(t, run)
				return
			}
			require.ErrorIs(t, run, engine.ErrNoCredential)
			require.ErrorIs(t, init, engine.ErrNoCredential)
			fix := remedyOf(t, run)
			assert.Equal(t, fix, remedyOf(t, init), "init's fix is the run's")
			assert.Equal(t, DoctorWarn, row.Status, "a missing credential is doctor's fail-loud row")
			assert.Equal(t, fix, row.Remedy, "doctor's fix is the run's")
			assert.Contains(t, row.Detail, fix)
			assert.Contains(t, row.Detail, claude.EngineName, "names the backend")
			assert.Contains(t, row.Detail, string(engine.AuthToken), "names the mode")
			assert.Contains(t, row.Detail, claude.OAuthTokenEnv, "names the variable")
		})
	}
}

// TestCredentialStatus_ResolvesTheConfiguredMode: a config declaring `auth:
// login` is asked for a login, not for the token. A hard-coded token mode
// refused a signed-in human for want of a variable they never needed, and
// told a human with no login to export one.
func TestCredentialStatus_ResolvesTheConfiguredMode(t *testing.T) {
	reg := engines.Registry()

	t.Run("signed in, no token: clean", func(t *testing.T) {
		cfg := claudeCredentialProject(t, engine.AuthLogin, true)
		checks := ConfiguredCredentialChecks(reg, cfg, envWith(nil))
		require.NotEmpty(t, checks)
		for _, c := range checks {
			assert.Equal(t, engine.AuthLogin, c.Mode)
			assert.NoError(t, c.Err, c.Backend)
		}
		assert.Equal(t, DoctorOK, doctorCheckCredentials(reg, cfg, envWith(nil)).Status)
		assert.NoError(t, AgentCredentialMissing(reg, claude.EngineName, cfg.SessionAuth(), envWith(nil)))
	})

	t.Run("not signed in: refuses as a login", func(t *testing.T) {
		cfg := claudeCredentialProject(t, engine.AuthLogin, false)
		row := doctorCheckCredentials(reg, cfg, envWith(nil))
		assert.Equal(t, DoctorWarn, row.Status)
		assert.Contains(t, row.Detail, "credential store", "the login the human is missing, not a variable")
		assert.NotContains(t, row.Detail, claude.OAuthTokenEnv+" is not exported")
		err := AgentCredentialMissing(reg, claude.EngineName, cfg.SessionAuth(), envWith(nil))
		require.ErrorIs(t, err, engine.ErrNoCredential)
		assert.NotContains(t, err.Error(), claude.OAuthTokenEnv)
	})
}
