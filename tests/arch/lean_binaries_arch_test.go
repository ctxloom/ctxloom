//go:build arch

package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// The companion binaries resolve engine names through the process-wide
// alias table, which is populated by composition — so each composes the
// LEAN name root (internal/lm/enginenames), never the descriptor root. The
// descriptors are typed on the bundle model, which those binaries were kept
// free of deliberately. This gate holds that line as a checked fact rather
// than a measurement someone did once: the lean binaries' transitive link set
// must contain neither the descriptor package nor the bundle model.
func TestArch_LeanBinaries_DoNotLinkEngineDescriptors(t *testing.T) {
	forbidden := []string{
		modulePath + "/internal/lm/engine",
		modulePath + "/internal/lm/engines",
		modulePath + "/internal/lm/backends",
		modulePath + "/internal/bundles",
	}
	for _, bin := range []string{"./cmd/ltk", "./cmd/taskloom"} {
		t.Run(bin, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", bin)
			cmd.Dir = moduleRoot(t)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps %s: %v", bin, err)
			}
			deps := strings.Fields(string(out))
			if len(deps) == 0 {
				t.Fatalf("go list -deps %s reported nothing — the gate has nothing to check", bin)
			}
			for _, dep := range deps {
				for _, f := range forbidden {
					if dep == f {
						t.Errorf("%s links %s — the lean binaries compose internal/lm/enginenames, never the descriptors or the bundle model they are typed on", bin, dep)
					}
				}
			}
		})
	}
}
