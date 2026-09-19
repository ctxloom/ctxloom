//go:build arch

package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// The file-lock and atomic-write primitives are toolbox leaves, not engine
// base: a package that only needs to lock and rewrite a config file must not
// link the engine base — and through it the engine vocabulary — to do so.
// confpatch is the family binaries' config patcher and profiles is a core
// leaf; neither has any business importing core/agent.
func TestArch_ToolboxPrimitives_ConfpatchAndProfilesDoNotLinkTheEngineBase(t *testing.T) {
	forbidden := modulePath + "/internal/core/agent"
	for _, pkg := range []string{"./internal/adapters/confpatch", "./internal/core/profiles"} {
		t.Run(pkg, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", pkg)
			cmd.Dir = moduleRoot(t)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps %s: %v", pkg, err)
			}
			deps := strings.Fields(string(out))
			if len(deps) == 0 {
				t.Fatalf("go list -deps %s reported nothing — the gate has nothing to check", pkg)
			}
			for _, dep := range deps {
				if dep == forbidden {
					t.Errorf("%s links %s — the lock and atomic-write primitives it needs live in the toolbox", pkg, dep)
				}
			}
		})
	}
}
