//go:build arch

package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// TestArch_CoordLinksNoAdapter pins the runtime coordinator's ring from the
// LINK side: the transitive dependency closure of internal/core/coord holds
// neither half of what left it — the wire (adapters/coordgrpc and its proto
// and endpoint file) nor the engine host (adapters/runner and the transcript
// recorder it took with it). The layering gate reads direct imports per
// package; this one reads what the compiler actually links, so a package
// that reaches core/coord back through a toolbox package, or a file that
// drags the proto in behind a domain type, fails here even where each direct
// edge looks innocent.
//
// The two adapters named are the two this ring rule exists for: coordgrpc
// terminates the wire and CALLS the verbs, runner drives the engine and
// DIALS the coordinator — both import core/coord, so core/coord importing
// either is an import cycle waiting for the innocent change that completes
// it (the same reversal the cycle-prevention rows in archrules.LayeringRules
// pin for the external-test-package shapes). Other adapters in the closure
// are other core packages' allowlisted edges, each ratcheted by its own row
// and TestArch_LayeringAllowlist_IsLive; they are not this gate's.
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
	forbidden := []string{
		modulePath + "/internal/adapters/coordgrpc",
		modulePath + "/internal/adapters/runner",
		modulePath + "/internal/adapters/transcript",
	}
	for _, dep := range deps {
		for _, f := range forbidden {
			if dep == f || strings.HasPrefix(dep, f+"/") {
				t.Errorf("internal/core/coord links %s — the coordinator is core; the wire is adapters/coordgrpc's and the engine host is adapters/runner's, and both of those import core/coord", dep)
			}
		}
	}
}
