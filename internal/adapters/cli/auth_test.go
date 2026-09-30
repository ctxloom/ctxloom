package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const cliFixtureToken = "sk-ant-oat01-cli-fixture"

// authEnv isolates the environment: a scratch HOME and none of claude's
// credential vars exported.
func authEnv(t *testing.T) {
	t.Helper()
	testsupport.Isolate(t)
	for _, v := range []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv} {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
}

// ctxloom stores no credential, so `auth` carries no command that takes
// one: status is all there is.
func TestAuth_HasNoCredentialCapture(t *testing.T) {
	var subs []string
	for _, c := range authCmd.Commands() {
		subs = append(subs, c.Name())
	}
	assert.Equal(t, []string{"status"}, subs)
}

// The help instructs: the human mints the token with claude's own
// setup-token and exports it; ctxloom never collects it; subscription agents
// share the human's usage limits.
func TestAuth_HelpInstructsTheHumanToMintAndExport(t *testing.T) {
	for _, want := range []string{"claude setup-token", claude.OAuthTokenEnv, "secret manager", "never", "usage limits"} {
		assert.Contains(t, authLong, want)
	}
	assert.NotContains(t, authLong, "auth mint")
	assert.NotContains(t, authLong, "auth set")
}

// status: per engine and env-carried mode, whether the credential is
// present in the environment — the var names, never a value — and, when it
// is not, the engine's remedy. The login is not an environment credential,
// so it has no row.
func TestAuthStatus_ReportsWhetherEachCredentialIsInTheEnvironment(t *testing.T) {
	authEnv(t)
	t.Setenv(claude.OAuthTokenEnv, cliFixtureToken)

	out, err := runRoot(t, "auth", "status", "--format", formatText)
	require.NoError(t, err, out)
	assert.Contains(t, out, "claude-code token: present ("+claude.OAuthTokenEnv+")\n")
	assert.Contains(t, out, "claude-code api-key: missing — export "+claude.APIKeyEnv+"\n")
	assert.NotContains(t, out, "claude-code login", "the login is not read from the environment")
	assert.NotContains(t, out, cliFixtureToken)

	js, err := runRoot(t, "auth", "status", "--format", "json")
	require.NoError(t, err, js)
	assert.NotContains(t, js, cliFixtureToken)
	var rows []authStatusRow
	require.NoError(t, json.Unmarshal([]byte(js), &rows), js)
	byMode := map[string]authStatusRow{}
	for _, r := range rows {
		if r.Engine == claude.EngineName {
			byMode[r.Mode] = r
		}
	}
	assert.Equal(t, authStatusRow{Engine: claude.EngineName, Mode: "token", Present: true, Vars: []string{claude.OAuthTokenEnv}}, byMode["token"])
	assert.Equal(t, authStatusRow{Engine: claude.EngineName, Mode: "api-key", Remedy: "export " + claude.APIKeyEnv}, byMode["api-key"])
	assert.NotContains(t, byMode, "login")
}

// With no token exported, status names how the human mints one.
func TestAuthStatus_AMissingTokenNamesSetupToken(t *testing.T) {
	authEnv(t)
	out, err := runRoot(t, "auth", "status", "--format", formatText)
	require.NoError(t, err, out)
	assert.Contains(t, out, "claude-code token: missing — run `claude setup-token` and export "+claude.OAuthTokenEnv)
}
