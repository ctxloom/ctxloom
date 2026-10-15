// Package mutation holds the tests of the mutation-testing plumbing: the
// recipes, the scripts they release gomutants through, and (under
// -tags mutation, which needs gomutants on PATH) the real tool driven through
// scripts/mutshard.
package mutation

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRootFromTest walks up to the module root so the tests can invoke the
// scripts by their repo-relative paths regardless of where `go test` is run.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from the test's working directory")
		}
		dir = parent
	}
}

// repoInput returns the absolute path of each repo-relative file (globs
// allowed) a SUBPROCESS of this test will read, after reading it here.
//
// The read is the point. `go test` caches a passing result keyed on the files
// the test binary itself opened; a script run through exec.Command is read by
// bash, not by the binary, so without this an edit to the script — deleting the
// very check the test pins — is answered with the cached PASS.
func repoInput(t *testing.T, rel ...string) []string {
	t.Helper()
	root := repoRootFromTest(t)
	var paths []string
	for _, r := range rel {
		matches, err := filepath.Glob(filepath.Join(root, r))
		if err != nil || len(matches) == 0 {
			t.Fatalf("no repo file matches %s: %v", r, err)
		}
		for _, m := range matches {
			if _, err := os.ReadFile(m); err != nil {
				t.Fatalf("read %s: %v", m, err)
			}
		}
		paths = append(paths, matches...)
	}
	return paths
}
