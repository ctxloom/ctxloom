package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

const cliFixtureToken = "sk-ant-oat01-cli-fixture"

func authHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(claude.OAuthTokenEnv, "")
	require.NoError(t, os.Unsetenv(claude.OAuthTokenEnv))
}

// set-token reads the token from stdin, stores it owner-only, and never
// prints it back.
func TestAuthSetToken_StoresFromStdinAndNeverPrintsIt(t *testing.T) {
	authHome(t)
	rootCmd.SetIn(strings.NewReader(cliFixtureToken + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	out, err := runRoot(t, "auth", "set-token")
	require.NoError(t, err, out)
	assert.NotContains(t, out, cliFixtureToken)
	assert.Contains(t, out, claude.OAuthTokenEnv)

	path, err := paths.HomeEngineTokenPath(claude.EngineName)
	require.NoError(t, err)
	assert.Contains(t, out, path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, cliFixtureToken, string(got))
}

func TestAuthSetToken_RefusesEmptyInput(t *testing.T) {
	authHome(t)
	rootCmd.SetIn(strings.NewReader("\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	_, err := runRoot(t, "auth", "set-token")
	require.ErrorIs(t, err, isolation.ErrEmptyToken)
}

// The entry point exports the stored token before any command runs, and
// status reports where the var's value came from without the value.
func TestRun_ExportsTheStoredTokenBeforeAnyCommand(t *testing.T) {
	authHome(t)
	rootCmd.SetIn(strings.NewReader(cliFixtureToken))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	_, err := runRoot(t, "auth", "set-token")
	require.NoError(t, err)

	var out bytes.Buffer
	require.Equal(t, 0, RunWithArgs(testComposition(), []string{"auth", "status", "--format", "json"}, &out), out.String())
	assert.NotContains(t, out.String(), cliFixtureToken)
	assert.Equal(t, cliFixtureToken, os.Getenv(claude.OAuthTokenEnv))
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
	require.NotEmpty(t, rows)
	var row map[string]any
	for _, r := range rows {
		if r["engine"] == claude.EngineName {
			row = r
		}
	}
	require.NotNil(t, row, out.String())
	assert.Equal(t, true, row["stored"])
	assert.Equal(t, "0600", row["mode"])
	assert.Equal(t, "stored", row["source"])
}
