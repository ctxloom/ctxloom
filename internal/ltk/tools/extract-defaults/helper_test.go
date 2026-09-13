package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// moduleFile resolves rel (a module-root-relative path) against the module
// root, found by walking up from where the test binary STARTED rather than
// from the current working directory.
//
// The distinction is the whole point of the test that uses it. A cwd-relative
// walk is how a gate that reads the source tree evaporates: move the working
// directory and it finds nothing, asserts nothing, and reports success — a
// clean sweep that swept nothing. The starting directory does not move, so the
// only remaining failure is a genuinely missing file, which is a failure and
// is reported as one.
func moduleFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := sourcedir.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, rel)
}

// readModuleFile reads a module-root-relative path, failing the test if it
// cannot. It never skips: a drift gate that can skip itself is not a gate.
func readModuleFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(moduleFile(t, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}
