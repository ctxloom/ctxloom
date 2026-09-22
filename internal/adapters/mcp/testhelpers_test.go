package mcp

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// Strict-state helpers for this package's tests — a verbatim sibling of the
// copy other packages that touch the process-wide sinks carry, rather than a
// shared test-only package every test package would then import.

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors strictness_test.go's resetForTest).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

// pkgSourceDir is this package's own source directory, resolved from the
// compiled-in path of this file rather than from the process cwd (a test that
// chdirs would otherwise scan the wrong tree, or none at all, and report a
// clean sweep).
func pkgSourceDir(t *testing.T) string {
	t.Helper()
	dir, err := sourcedir.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
