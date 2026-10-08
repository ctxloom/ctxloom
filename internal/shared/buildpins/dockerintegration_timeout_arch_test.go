//go:build arch

package buildpins

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// dockerIntegrationGoTestRE finds a `go test` command line built with the
// docker_integration tag (quoted or bare, alone or among other tags).
var dockerIntegrationGoTestRE = regexp.MustCompile(`go test\b[^\n]*-tags[ =]"?[^"\n]*\bdocker_integration\b`)

// TestArch_DockerIntegrationRecipesSetAnExplicitTimeout guards the
// docker-gated container suites against go test's implicit 10-minute alarm.
//
// Those packages iterate EVERY reachable runtime (docker, then podman), so
// their wall time scales with what the host has installed, not with the code:
// on a host carrying both runtimes internal/core/coord alone measured past
// ten minutes, and the default alarm panicked it mid-subtest — a required
// gate leg reporting a timeout stack instead of which test failed. An explicit
// -timeout on the recipe line is the budget being a decision, not a default.
func TestArch_DockerIntegrationRecipesSetAnExplicitTimeout(t *testing.T) {
	found := 0
	for _, path := range justfilesDefiningRecipes {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || !dockerIntegrationGoTestRE.MatchString(line) {
				continue
			}
			found++
			if !strings.Contains(line, "-timeout") {
				t.Errorf("%s:%d runs the docker_integration suite with go test's default 10m alarm; give it an explicit -timeout:\n  %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if found == 0 {
		t.Fatal("no docker_integration go test line found in any justfile — this gate would pass vacuously")
	}
}
