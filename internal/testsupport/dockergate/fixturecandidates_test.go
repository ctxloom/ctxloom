package dockergate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestFixtureCandidates_AreOutsideTheSourceTree pins the property the leak gate
// cares about, in the DEFAULT lane — where the gate actually runs. The
// docker-gated tests that root fixtures here only run with a daemon and a
// build tag, so a regression would be invisible to `just test` until it had
// already written into the checkout.
func TestFixtureCandidates_AreOutsideTheSourceTree(t *testing.T) {
	t.Setenv("RUNNER_TEMP", filepath.Join(t.TempDir(), "runner-temp"))
	repo, err := sourcedir.RepoRoot()
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	for _, c := range FixtureCandidates() {
		if !filepath.IsAbs(c) {
			t.Fatalf("fixture candidate %q must be absolute: a relative root resolves against the test binary's cwd, which IS the package source dir", c)
		}
		if rel, err := filepath.Rel(repo, c); err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			t.Fatalf("fixture candidate %q is inside the source tree %q: the leak gate scans the checkout", c, repo)
		}
	}
}
