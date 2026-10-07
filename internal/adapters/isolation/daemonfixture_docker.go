//go:build docker_integration

package isolation

import "fmt"

// This file is compiled only into the docker-gated test suites: it is how a
// docker_integration test outside this package names its bind sources the way
// production does, through this process's own layer. It imports nothing from
// internal/testsupport (archlint.TestSupportAnalyzer) and reads no
// environment, so the skip and the candidate directories live there
// (daemonfixture, dockergate.FixtureCandidates).

// DaemonLayer is this process's own layer as the daemon of the runtime named
// runtime reports it (primaryLayer): the layer production reverses every bind
// source through. An undecidable self is an error, never the host layer, for
// the reason settleSelf refuses one.
func DaemonLayer(runtime string) (Layer, error) {
	for _, c := range runtimeCandidates() {
		if c.name != runtime {
			continue
		}
		rt, _ := c.probe()
		if err := rt.identified(); err != nil {
			return Layer{}, err
		}
		return rt.primary(), nil
	}
	return Layer{}, fmt.Errorf("isolation: no container runtime named %q", runtime)
}

// FixtureRoot is the first non-empty candidate l names in the daemon's path
// space: the directory a docker-gated test roots its fixtures under. A test
// that binds a directory the daemon cannot name hands it a source it creates
// EMPTY, so no candidate is ErrUnmapped, never a fallback.
func FixtureRoot(l Layer, candidates ...string) (string, error) {
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
