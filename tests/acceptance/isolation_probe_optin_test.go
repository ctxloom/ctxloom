//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeOptInFixture stages the box the gate must refuse on: a claude binary on
// PATH that reports itself logged in, a host credential file the probe would
// happily copy, and no token captured at launch. Every invocation of the fake
// binary appends one line to the returned counter file.
func probeOptInFixture(t *testing.T) (countFile string) {
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
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "")
	t.Setenv("CTXLOOM_LIVE_REQUIRE", "")
	withLaunchCredentials(t, map[string]string{})
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

// Without the opt-in, an isolation-probe cell must skip before it touches the
// engine at all — even though a host credential file would otherwise carry it
// onto the seeded path and into a paid turn.
func TestProbeTargetAuth_NoOptInSkipsWithZeroEngineCalls(t *testing.T) {
	for _, axis := range []probeAxis{probeAxisWorktree, probeAxisContainerRootless, probeAxisContainerRootful} {
		t.Run(string(axis), func(t *testing.T) {
			countFile := probeOptInFixture(t)

			path, reason, err := probeTargetAuth("claude-code", axis)
			require.NoError(t, err)

			assert.Equal(t, probeAuthNone, path, "reason: %s", reason)
			assert.Contains(t, reason, "CTXLOOM_ACCEPTANCE_LIVE=1 not set")
			assert.Empty(t, probeInvocations(t, countFile), "the engine binary was invoked without the opt-in")
		})
	}
}

// Opted in, the same box still reaches the seeded path, through exactly the
// one authCheck every other @live cell's gate makes.
func TestProbeTargetAuth_OptedInStillReachesSeededPath(t *testing.T) {
	countFile := probeOptInFixture(t)
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "1")

	path, reason, err := probeTargetAuth("claude-code", probeAxisWorktree)
	require.NoError(t, err)

	assert.Equal(t, probeAuthSeeded, path, "reason: %s", reason)
	assert.Equal(t, []string{"auth status"}, probeInvocations(t, countFile))
}

func TestProbeTargetAuth_UnknownAxisIsAnError(t *testing.T) {
	probeOptInFixture(t)
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "1")

	_, _, err := probeTargetAuth("claude-code", probeAxis("sideways"))
	assert.Error(t, err)
}
