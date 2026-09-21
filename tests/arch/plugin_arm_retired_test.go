//go:build arch

package arch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestArch_PluginArmRetired pins that the go-plugin arm is gone end to end:
// the plugin protocol package (internal/lm/grpc) and the two transports over
// it (internal/vpio/goplugin, internal/vpio/dockerexec) exist nowhere in the
// module's package list, nothing links them, and go.mod no longer requires
// hashicorp/go-plugin. The runner is the ONE unit a launch runs in
// (adapters/runner.Main, started by adapters/spawn.StartRunner for host and
// container alike); a second process protocol beside it is what this gate
// refuses.
//
// `go list` is the authority because an import that compiles is the only
// binding that matters: a stale doc naming lm/grpc is prose, an import of it
// is a second arm.
func TestArch_PluginArmRetired(t *testing.T) {
	root := moduleRoot(t)
	retired := []string{
		modulePath + "/internal/lm/grpc",
		modulePath + "/internal/vpio/goplugin",
		modulePath + "/internal/vpio/dockerexec",
	}

	list := exec.Command("go", "list", "./...")
	list.Dir = root
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		t.Fatal("go list ./... reported nothing — the gate has nothing to check")
	}
	for _, pkg := range pkgs {
		for _, r := range retired {
			if pkg == r || strings.HasPrefix(pkg, r+"/") {
				t.Errorf("%s still exists — the plugin arm is retired; the runner is the one unit", pkg)
			}
		}
	}

	deps := exec.Command("go", "list", "-deps", "./cmd/...", "./internal/...")
	deps.Dir = root
	out, err = deps.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	linked := strings.Fields(string(out))
	if len(linked) == 0 {
		t.Fatal("go list -deps reported nothing — the gate has nothing to check")
	}
	for _, dep := range linked {
		if strings.HasPrefix(dep, "github.com/hashicorp/go-plugin") {
			t.Errorf("%s is still linked — no process in this module speaks go-plugin", dep)
		}
	}

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if strings.Contains(string(gomod), "github.com/hashicorp/go-plugin") {
		t.Error("go.mod still requires github.com/hashicorp/go-plugin — the dependency leaves with the arm")
	}
}
