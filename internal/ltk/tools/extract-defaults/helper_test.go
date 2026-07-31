package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// moduleFile resolves rel (a module-root-relative path) against the module
// root, located from THIS FILE's compiled-in source path rather than the
// process working directory.
//
// The distinction is the whole point of the test that uses it. A cwd-relative
// walk is how a gate that reads the source tree evaporates: move the working
// directory and it finds nothing, asserts nothing, and reports success — a
// clean sweep that swept nothing. runtime.Caller gives a path that does not
// move, so the only remaining failure is a genuinely missing file, which is a
// failure and is reported as one.
func moduleFile(t *testing.T, rel string) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own source file")
	}
	for dir := filepath.Dir(self); ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(self))
		}
		dir = parent
	}
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
