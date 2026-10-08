//go:build docker_integration

package isolation

// installHermeticRuntimes leaves the production candidate table in place: the
// docker_integration suite exists to talk to a real runtime.
func installHermeticRuntimes() {}
