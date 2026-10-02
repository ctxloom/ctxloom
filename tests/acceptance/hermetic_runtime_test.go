//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHermeticLaneReachesNoHostContainerRuntime pins that the hermetic lane
// cannot reach the host's real docker/podman, while the container lane still
// does. The "host runtime" is a counting shim put AHEAD of the inherited PATH
// before the scenario environment is built — exactly where a developer's real
// /usr/bin/docker sits from the harness's point of view — so the test reads
// the same on a machine with no runtime installed. `ctxloom doctor` is the
// probe: it asks every runtime `info` on each run.
func TestHermeticLaneReachesNoHostContainerRuntime(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	hostBin := t.TempDir()
	for _, name := range []string{"docker", "podman"} {
		shim := "#!/bin/sh\necho \"" + name + " $*\" >> '" + calls + "'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(hostBin, name), []byte(shim), 0o755)) //nolint:gosec // must be executable
	}
	t.Setenv("PATH", hostBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	countDoctorCalls := func(hostRuntimes bool) int {
		require.NoError(t, os.WriteFile(calls, nil, 0o600))
		env, err := newScenarioEnv(hostRuntimes)
		require.NoError(t, err)
		defer func() { require.NoError(t, env.Cleanup()) }()
		_ = env.Run("doctor") // its exit status is not the subject; who it called is
		got, err := os.ReadFile(calls)
		require.NoError(t, err)
		return strings.Count(string(got), "\n")
	}

	require.Zero(t, countDoctorCalls(false), "a hermetic scenario's doctor called a host container runtime")
	require.Positive(t, countDoctorCalls(true), "the container lane must still reach the host's runtimes; zero calls means the shim, not the lane, is broken")
}
