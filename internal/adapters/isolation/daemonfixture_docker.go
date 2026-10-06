//go:build docker_integration

package isolation

import (
	"fmt"
	"os"
)

// This file is compiled only into the docker-gated test suites: it is how a
// docker_integration test outside this package names its bind sources the way
// production does, through this process's own layer. It imports nothing from
// internal/testsupport (archlint.TestSupportAnalyzer), so the skip and the
// environment handling live in internal/testsupport/daemonfixture.

// DaemonLayer is this process's own layer as runtime's daemon reports it
// (primaryLayer): the layer production reverses every bind source through.
// An unidentified self is an error, never the host layer, for the reason
// settleSelf refuses one.
func DaemonLayer(runtime string) (Layer, error) {
	var rt Runtime
	switch runtime {
	case "docker":
		rt, _ = newDockerRuntime(runtimeReachable)
	case "podman":
		rt, _ = newPodmanRuntime(runtimeReachable)
	default:
		return Layer{}, fmt.Errorf("isolation: no container runtime named %q", runtime)
	}
	if err := rt.identified(); err != nil {
		return Layer{}, err
	}
	return rt.primary(), nil
}

// FixtureRoot is the directory a docker-gated test roots its fixtures under:
// the process temp dir when l names it in the daemon's path space (the
// daemon's own host, or a container sharing it), else $RUNNER_TEMP — the
// runner directory a CI job container is given from the host, at a path of
// its own. A test that binds a private directory hands the daemon a source
// it creates EMPTY, so no candidate is ErrUnmapped, never a fallback.
func FixtureRoot(l Layer) (string, error) {
	return fixtureRoot(l, []string{os.TempDir(), os.Getenv("RUNNER_TEMP")})
}

// fixtureRoot is the first non-empty candidate l reverses.
func fixtureRoot(l Layer, candidates []string) (string, error) {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := l.Reverse(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: no fixture root among %q", ErrUnmapped, candidates)
}
