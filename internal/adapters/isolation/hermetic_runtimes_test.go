//go:build !docker_integration

package isolation

// installHermeticRuntimes makes "no container runtime is reachable" the unit
// suite's default: every launch survey, SelectRuntime and ProbeRuntime walks
// an EMPTY candidate table and answers Host{} without exec'ing docker or
// podman. Left at the production table, a unit test's verdict would be a
// report on whatever daemon the machine running it has (and a slow daemon its
// runtime). Tests that need a runtime hand one in (stubRuntimeProbe,
// stubRuntimeCandidates, or useProductionRuntimeCandidates over a PATH shim);
// tests that need a REAL one carry the docker_integration tag, whose build
// keeps the production table (hermetic_runtimes_docker_test.go).
func installHermeticRuntimes() {
	runtimeCandidates = func() []runtimeCandidate { return nil }
}
