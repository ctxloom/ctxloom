package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

const cliFixtureToken = "sk-ant-oat01-cli-fixture"

// authHome points HOME at scratch, clears claude's credential vars, and
// resets the auth commands' package-global flags after the test.
func authHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv} {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	t.Cleanup(func() { authEngine, authMode = "", ""; rootCmd.SetIn(nil) })
	return home
}

func credentialPath(t *testing.T, mode engine.AuthMode) string {
	t.Helper()
	p, err := paths.HomeEngineCredentialPath(claude.EngineName, string(mode))
	require.NoError(t, err)
	return p
}

// set reads the credential from stdin, stores it owner-only for the named
// mode, and never prints it back.
func TestAuthSet_StoresFromStdinAndNeverPrintsIt(t *testing.T) {
	authHome(t)
	rootCmd.SetIn(strings.NewReader("sk-ant-api03-key\n"))
	out, err := runRoot(t, "auth", "set", "--mode", "api-key")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "sk-ant-api03-key")
	path := credentialPath(t, engine.AuthAPIKey)
	assert.Contains(t, out, path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-key", string(got))
}

func TestAuthSet_RefusesEmptyInput(t *testing.T) {
	authHome(t)
	rootCmd.SetIn(strings.NewReader("\n"))
	_, err := runRoot(t, "auth", "set", "--mode", "token")
	require.ErrorIs(t, err, isolation.ErrEmptyCredential)
}

// A credential typed as an argument is refused without being repeated and
// without anything being stored: argv lands in shell history and the
// process list.
func TestAuthCredentialCommands_RefuseASecretOnArgv(t *testing.T) {
	for _, sub := range []string{"set", "mint"} {
		t.Run(sub, func(t *testing.T) {
			authHome(t)
			out, err := runRoot(t, "auth", sub, "--mode", "token", cliFixtureToken)
			require.ErrorIs(t, err, errSecretOnArgv)
			assert.NotContains(t, out, cliFixtureToken)
			assert.NotContains(t, err.Error(), cliFixtureToken)
			assert.NoFileExists(t, credentialPath(t, engine.AuthToken))
		})
	}
}

// fakeClaudeOnPath puts an executable `claude` first on PATH whose
// setup-token prints a token; it never reaches a real login.
func fakeClaudeOnPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = setup-token ] || exit 9\necho 'Your OAuth token:'\necho '%s'\n", cliFixtureToken)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
}

// withAuthTerminal sets what authTerminal answers for the test.
func withAuthTerminal(t *testing.T, term engine.Terminal, ok bool) {
	t.Helper()
	prev := authTerminal
	authTerminal = func(*cobra.Command) (engine.Terminal, bool) { return term, ok }
	t.Cleanup(func() { authTerminal = prev })
}

// mint runs the engine's own flow on the terminal and stores what it
// produced; the command's output names where, never what.
func TestAuthMint_RunsTheEnginesFlowAndStoresTheToken(t *testing.T) {
	authHome(t)
	fakeClaudeOnPath(t)
	var flow, flowErr bytes.Buffer
	withAuthTerminal(t, engine.Terminal{In: strings.NewReader(""), Out: &flow, Err: &flowErr}, true)
	out, err := runRoot(t, "auth", "mint", "--mode", "token")
	require.NoError(t, err, out)
	assert.NotContains(t, out, cliFixtureToken, "the result names where, never what")
	assert.Contains(t, flow.String(), "Your OAuth token", "the engine's flow reached the terminal")
	got, err := os.ReadFile(credentialPath(t, engine.AuthToken))
	require.NoError(t, err)
	assert.Equal(t, cliFixtureToken, string(got))
}

func TestAuthMint_NeedsATerminal(t *testing.T) {
	authHome(t)
	withAuthTerminal(t, engine.Terminal{}, false)
	_, err := runRoot(t, "auth", "mint", "--mode", "token")
	require.ErrorIs(t, err, errMintNeedsTerminal)
}

// A key cannot be minted; the refusal names the command that stores one.
func TestAuthMint_AnUnmintableModeNamesSet(t *testing.T) {
	authHome(t)
	withAuthTerminal(t, engine.Terminal{In: strings.NewReader("")}, true)
	_, err := runRoot(t, "auth", "mint", "--mode", "api-key")
	require.ErrorIs(t, err, engine.ErrMintUnsupported)
	assert.Contains(t, err.Error(), "ctxloom auth set --engine claude-code --mode api-key")
}

// status on unix: one line per stored-credential mode, the mode bits in
// parentheses, never the credential; JSON carries the same.
func TestAuthStatus_ReportsEachModeWithItsProtection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows reports the ACL verdict, asserted in isolation's windows tests")
	}
	authHome(t)
	_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(cliFixtureToken))
	require.NoError(t, err)

	out, err := runRoot(t, "auth", "status", "--format", formatText)
	require.NoError(t, err, out)
	assert.Contains(t, out, fmt.Sprintf("claude-code token: stored at %s (mode 0600)\n", credentialPath(t, engine.AuthToken)))
	assert.Contains(t, out, fmt.Sprintf("claude-code api-key: none stored at %s\n", credentialPath(t, engine.AuthAPIKey)))
	assert.NotContains(t, out, "login", "the login is never stored")
	assert.NotContains(t, out, cliFixtureToken)

	js, err := runRoot(t, "auth", "status", "--format", "json")
	require.NoError(t, err, js)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(js), &rows), js)
	var token map[string]any
	for _, r := range rows {
		if r["engine"] == claude.EngineName && r["mode"] == "token" {
			token = r
		}
	}
	require.NotNil(t, token, js)
	assert.Equal(t, true, token["stored"])
	assert.Equal(t, "mode 0600", token["protection"])
}
