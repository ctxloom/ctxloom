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
	reportRetiredPackages(t, goListFields(t, root, "go list ./...", "list", "./..."), retired)
	reportGoPluginLinked(t, goListFields(t, root, "go list -deps", "list", "-deps", "./cmd/...", "./internal/..."))

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if strings.Contains(string(gomod), "github.com/hashicorp/go-plugin") {
		t.Error("go.mod still requires github.com/hashicorp/go-plugin — the dependency leaves with the arm")
	}
}

// goListFields runs `go <args>` in root and returns its output's fields,
// fatal (under label) when it fails or reports nothing.
func goListFields(t *testing.T, root, label string, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatalf("%s reported nothing — the gate has nothing to check", label)
	}
	return fields
}

// reportRetiredPackages fails for every package at or under a retired path.
func reportRetiredPackages(t *testing.T, pkgs, retired []string) {
	t.Helper()
	for _, pkg := range pkgs {
		for _, r := range retired {
			if pkg == r || strings.HasPrefix(pkg, r+"/") {
				t.Errorf("%s still exists — the plugin arm is retired; the runner is the one unit", pkg)
			}
		}
	}
}

// reportGoPluginLinked fails for every linked go-plugin package.
func reportGoPluginLinked(t *testing.T, linked []string) {
	t.Helper()
	for _, dep := range linked {
		if strings.HasPrefix(dep, "github.com/hashicorp/go-plugin") {
			t.Errorf("%s is still linked — no process in this module speaks go-plugin", dep)
		}
	}
}
