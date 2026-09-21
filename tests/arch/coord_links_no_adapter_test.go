//go:build arch

package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// TestArch_CoordLinksNoAdapter pins the runtime coordinator's ring from the
// LINK side: the transitive dependency closure of internal/core/coord holds no
// package under internal/adapters. The layering gate reads direct imports per
// package; this one reads what the compiler actually links, so an adapter that
// reaches core/coord through a toolbox package, or a runner-side file that
// drags the proto back in, fails here even where each direct edge looks
// innocent.
//
// The two adapters named in the message are the two halves this ring rule
// exists for: coordgrpc terminates the wire and CALLS the verbs, runner drives
// the engine and DIALS the coordinator — both import core/coord, so core/coord
// importing either is an import cycle waiting for the innocent change that
// completes it (the same reversal the cycle-prevention rows in
// archrules.LayeringRules pin for the external-test-package shapes).
func TestArch_CoordLinksNoAdapter(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./internal/core/coord")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./internal/core/coord: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list -deps ./internal/core/coord reported nothing — the gate has nothing to check")
	}
	const forbidden = modulePath + "/internal/adapters/"
	for _, dep := range deps {
		if strings.HasPrefix(dep, forbidden) {
			t.Errorf("internal/core/coord links %s — the coordinator is core; the wire is adapters/coordgrpc's and the engine host is adapters/runner's, and both of those import core/coord", dep)
		}
	}
}
