//go:build arch

package arch

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestArch_GremlinsRunsMutantsPerPackage pins .gremlins.yaml's
// unleash.integration to an EXPLICIT false.
//
// Invariant: a mutant must only ever run under its own package's tests.
// internal/adapters/spawn's startrunner tests call isolation's
// HostRunner.Kill, which performs the real /proc session sweep UNSCOPED inside
// spawn's test binary. That is safe only because gremlins, with integration
// off, runs each mutant against its own package's tests alone. With
// integration on, an isolation killSession mutant (a negated session filter,
// say) runs under spawn's tests and SIGKILLs every process in the CI
// container — which has already happened once, via isolation's own tests
// before they were scoped.
//
// The key must be PRESENT, not merely absent-and-defaulted: gremlins'
// default is false today, but a default is the upstream's to change on any
// upgrade, and nothing here would notice. No recipe or workflow passes
// --integration, so this key is the only switch.
func TestArch_GremlinsRunsMutantsPerPackage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(moduleRoot(t), ".gremlins.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Unleash struct {
			Integration *bool `yaml:"integration"`
		} `yaml:"unleash"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse .gremlins.yaml: %v", err)
	}
	switch integration := cfg.Unleash.Integration; {
	case integration == nil:
		t.Fatal(".gremlins.yaml has no unleash.integration key — set it to false explicitly " +
			"(it must be nested under unleash:, where gremlins reads it)")
	case *integration:
		t.Fatal(".gremlins.yaml sets unleash.integration: true — a mutant in isolation's " +
			"session sweep would then run under other packages' tests and can SIGKILL " +
			"every process on the runner; set it back to false")
	}
}
