package dockergate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestBindFixtureRoot_IsOutsideTheSourceTree pins the property the leak gate
// cares about, in the DEFAULT lane — where the gate actually runs. The
// docker-gated tests that consume this root only run with a daemon and a
// build tag, so a regression there would be invisible to `just test` until it
// had already written into the checkout.
func TestBindFixtureRoot_IsOutsideTheSourceTree(t *testing.T) {
	root := BindFixtureRoot()
	if !filepath.IsAbs(root) {
		t.Fatalf("fixture root %q must be absolute: a relative root resolves against the test binary's cwd, which IS the package source dir", root)
	}
	repo, err := sourcedir.RepoRoot()
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	if rel, err := filepath.Rel(repo, root); err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		t.Fatalf("fixture root %q is inside the source tree %q: the leak gate scans the checkout", root, repo)
	}
	// The CI job shares exactly one temp root with the runner's daemon, so a
	// fixture anywhere else is created EMPTY for the daemon, and the
	// container reads a blank directory, not an error.
	if root != os.TempDir() {
		t.Fatalf("fixture root %q is not the temp root %q, the one the CI job shares with the docker daemon", root, os.TempDir())
	}
	st, err := os.Stat(root)
	if err != nil {
		t.Fatalf("fixture root %q must exist and be usable: %v", root, err)
	}
	if !st.IsDir() {
		t.Fatalf("fixture root %q is not a directory", root)
	}
}
