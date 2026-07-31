package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoRootFromSource resolves the repository root from THIS test file's
// compiled-in source path, never from the process working directory. A scan
// rooted at "." evaporates to zero matches (and a vacuous pass) the moment a
// TestMain sandboxes the binary into a temp dir.
func repoRootFromSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must resolve this test's source path")
	// <root>/internal/shared/agent/symlink_noprod_test.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	// Assert the fixture is what it claims to be before asserting anything
	// about it: a scan root that is not the repo root proves nothing.
	require.FileExists(t, filepath.Join(root, "go.mod"), "scan root must be the repo root")
	return root
}

// TestSetExecutablePathForTesting_HasNoProductionCaller pins the invariant
// SetExecutablePathForTesting's doc comment asserts: "It has no production
// caller and must not acquire one." That invariant is not self-enforcing —
// production once did call it, via ApplyHooksRequest.ExecPath, and the memoized
// answer is a property of the running binary rather than a knob. Nothing but
// this test stops the field (or an equivalent) coming back.
//
// A non-test caller anywhere under internal/ or cmd/ fails here by NAME, so the
// regression is legible rather than latent.
func TestSetExecutablePathForTesting_HasNoProductionCaller(t *testing.T) {
	root := repoRootFromSource(t)

	const mutator = "SetExecutablePathForTesting"
	var offenders []string
	for _, top := range []string{"internal", "cmd"} {
		dir := filepath.Join(root, top)
		require.DirExists(t, dir, "scan subtree must exist")
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			body := string(b)
			// The declaration itself lives in this package; only CALLS count.
			if strings.Contains(body, "func "+mutator+"(") {
				return nil
			}
			// Match a CALL, not a prose mention: another package's doc comment
			// legitimately names this mutator when describing its own shape.
			if strings.Contains(body, mutator+"(") {
				rel, _ := filepath.Rel(root, p)
				offenders = append(offenders, rel)
			}
			return nil
		})
		require.NoError(t, err)
	}

	// Prove the scan reached real source rather than an empty subtree: the
	// declaration site must be visible to the very same walk.
	declSeen := false
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "func "+mutator+"(") {
			declSeen = true
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, declSeen, "the scan must actually reach %s's declaration; a scan that finds nothing proves nothing", mutator)

	require.Empty(t, offenders, "%s must have no production caller (found in: %v)", mutator, offenders)
}
