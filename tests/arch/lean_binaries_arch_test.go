//go:build arch

package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// The companion binaries resolve engine names by exact match against their
// own lean registries; they never link the composition root or the hosting
// records. The hosting records are typed on the bundle model, which those
// binaries were kept free of deliberately. This gate holds that line as a
// checked fact rather than a measurement someone did once: the lean
// binaries' transitive link set must contain neither the hosting package
// nor the bundle model.
func TestArch_LeanBinaries_DoNotLinkEngineDescriptors(t *testing.T) {
	forbidden := []string{
		modulePath + "/internal/lm/hosting",
		modulePath + "/internal/engines",
		modulePath + "/internal/lm/backends",
		modulePath + "/internal/core/bundles",
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
						t.Errorf("%s links %s — the lean binaries never link the descriptors or the bundle model they are typed on", bin, dep)
					}
				}
			}
		})
	}
}
