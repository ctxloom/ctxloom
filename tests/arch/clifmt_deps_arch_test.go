//go:build arch

package arch

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// clifmtAdapterOnlyDeps are the modules only clifmt's cobra adapter
// (pkg/clifmt/cobrafmt) may link: core clifmt and clidiag are usable by a
// program with any flag library, or none.
var clifmtAdapterOnlyDeps = []string{
	"github.com/spf13/cobra",
	"github.com/spf13/pflag",
	"golang.org/x/term",
}

// The layering rule clifmt-must-not-import-ctxloom sees in-repo imports only,
// so it cannot keep a third-party module out. This gate reads the full
// transitive link set from the go tool instead.
func TestArch_ClifmtCoreDoesNotLinkCobra(t *testing.T) {
	for _, pkg := range []string{"./pkg/clifmt", "./pkg/clifmt/clidiag"} {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range goListDeps(t, pkg) {
				if slices.Contains(clifmtAdapterOnlyDeps, dep) {
					t.Errorf("%s links %s; only pkg/clifmt/cobrafmt may", pkg, dep)
				}
			}
		})
	}
}

// The positive control: the adapter does link cobra, so the gate above is
// reading a real dependency list rather than passing on an empty one.
func TestArch_ClifmtCoreDoesNotLinkCobra_SeesTheAdapter(t *testing.T) {
	deps := goListDeps(t, "./pkg/clifmt/cobrafmt")
	for _, want := range clifmtAdapterOnlyDeps {
		if !slices.Contains(deps, want) {
			t.Errorf("go list -deps ./pkg/clifmt/cobrafmt lacks %s — the gate's dependency list is not what it checks", want)
		}
	}
}

func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatalf("go list -deps %s reported nothing", pkg)
	}
	return deps
}
