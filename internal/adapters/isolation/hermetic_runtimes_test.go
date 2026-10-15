//go:build !docker_integration

package isolation

import (
	"context"
	"errors"
)

// installHermeticRuntimes makes "no container runtime is reachable" the unit
// suite's default: every launch survey, SelectRuntime and ProbeRuntime walks
// an EMPTY candidate table and answers Host{} without exec'ing docker or
// podman. Left at the production table, a unit test's verdict would be a
// report on whatever daemon the machine running it has (and a slow daemon its
// runtime). Tests that need a runtime hand one in (stubRuntimeProbe,
// stubRuntimeCandidates, or useProductionRuntimeCandidates over a PATH shim);
// tests that need a REAL one carry the docker_integration tag, whose build
// keeps the production table (hermetic_runtimes_docker_test.go).
//
// The rootless-wedge check (Docker.daemonState) likewise sees no environment
// and no docker context, so a fixture's rootless Docker is never judged by the
// host's $XDG_RUNTIME_DIR; rootless_wedge_test.go installs its own.
func installHermeticRuntimes() {
	runtimeCandidates = func() []runtimeCandidate { return nil }
	rootlessState = rootlessStateProbe{
		getenv:      func(string) string { return "" },
		contextHost: func(context.Context, string) (string, error) { return "", errNoHermeticContext },
	}
}

// errNoHermeticContext is the hermetic suite's answer to a docker context
// lookup: there is no docker CLI here.
var errNoHermeticContext = errors.New("hermetic suite: no docker context")
