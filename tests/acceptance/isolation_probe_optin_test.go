//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// probeAuthFixture stages a box that carries every credential the token-only
// model no longer honours for an agent run: a claude binary on PATH that
// reports itself logged in, a host credential file, and the live opt-in. What
// is captured at launch is the caller's to set. Every invocation of the fake
// binary appends one line to the returned counter file.
func probeAuthFixture(t *testing.T, captured map[string]string) (countFile string) {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "home")
	countFile = filepath.Join(dir, "invocations")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"fake":true}`), 0o600))
	script := "#!/bin/sh\necho \"$*\" >> '" + countFile + "'\necho '{\"loggedIn\":true}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "1")
	t.Setenv("CTXLOOM_LIVE_REQUIRE", "")
	withLaunchCredentials(t, captured)
	saved := realHomeDir
	t.Cleanup(func() { realHomeDir = saved })
	realHomeDir = home
	return countFile
}

func probeInvocations(t *testing.T, countFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(countFile)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

var probeAllAxes = []probeAxis{probeAxisWorktree, probeAxisContainerRootless, probeAxisContainerRootful}

// A probe cell's run is an agent run, and an agent run authenticates with the
// token alone: a logged-in host and a credential file on disk carry it
// nowhere, so the cell skips naming the token — before the engine is touched.
func TestProbeTargetAuth_NoTokenSkipsWithZeroEngineCalls(t *testing.T) {
	for _, axis := range probeAllAxes {
		t.Run(string(axis), func(t *testing.T) {
			countFile := probeAuthFixture(t, map[string]string{})

			auth, err := probeTargetAuth("claude-code", axis)
			require.NoError(t, err)

			assert.False(t, auth.ok(), "reason: %s", auth.Reason)
			assert.Empty(t, auth.Env)
			assert.Contains(t, auth.Reason, claude.OAuthTokenEnv)
			assert.Empty(t, probeInvocations(t, countFile), "the engine binary was invoked for a cell that cannot run")
		})
	}
}

// An API key is not the token: production unsets it from every agent run, so
// a cell holding only a key would spend a turn on a run that cannot
// authenticate.
func TestProbeTargetAuth_APIKeyAloneIsNotTheToken(t *testing.T) {
	countFile := probeAuthFixture(t, map[string]string{claude.APIKeyEnv: "fixture-api-key"})

	auth, err := probeTargetAuth("claude-code", probeAxisWorktree)
	require.NoError(t, err)

	assert.False(t, auth.ok(), "reason: %s", auth.Reason)
	assert.Contains(t, auth.Reason, claude.OAuthTokenEnv)
	assert.Empty(t, probeInvocations(t, countFile))
}

// With the token captured, every axis gets exactly the token-mode credential
// production hands an agent run — the token, and no other credential var —
// with no engine call, and the printable reason never carries the value.
func TestProbeTargetAuth_TokenIsTheWholeCredentialOnEveryAxis(t *testing.T) {
	const token = "fixture-oauth-token"
	for _, axis := range probeAllAxes {
		t.Run(string(axis), func(t *testing.T) {
			countFile := probeAuthFixture(t, map[string]string{
				claude.OAuthTokenEnv: token,
				claude.APIKeyEnv:     "fixture-api-key",
			})

			auth, err := probeTargetAuth("claude-code", axis)
			require.NoError(t, err)

			require.True(t, auth.ok(), "reason: %s", auth.Reason)
			assert.Equal(t, map[string]string{claude.OAuthTokenEnv: token}, auth.Env)
			assert.NotContains(t, auth.Reason, token)
			assert.Empty(t, probeInvocations(t, countFile), "a captured token is its own proof of intent; no auth probe may run")
		})
	}
}

func TestProbeTargetAuth_UnknownAxisIsAnError(t *testing.T) {
	probeAuthFixture(t, map[string]string{claude.OAuthTokenEnv: "fixture-oauth-token"})

	_, err := probeTargetAuth("claude-code", probeAxis("sideways"))
	assert.Error(t, err)
}

func TestProbeTargetAuth_UnknownEngineSkips(t *testing.T) {
	probeAuthFixture(t, map[string]string{claude.OAuthTokenEnv: "fixture-oauth-token"})

	auth, err := probeTargetAuth("no-such-engine", probeAxisWorktree)
	require.NoError(t, err)
	assert.False(t, auth.ok())
	assert.Contains(t, auth.Reason, "no-such-engine")
}
