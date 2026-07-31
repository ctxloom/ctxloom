package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes the WHOLE internal/cli test binary — an isolated HOME and
// an isolated working directory, installed before a single test runs.
//
// It is not a nicety. These tests drive cobra RunE bodies, and a RunE resolves
// the package-level GetConfig(), which resolves the REAL app directory:
// config.findAppDir walks UP FROM THE WORKING DIRECTORY first, and a test
// binary's cwd is its own package source dir. On a machine where the checkout
// lives under $HOME (the ordinary developer layout) that walk-up reaches the
// user's own ~/.ctxloom and adopts it as the project — whereupon
// `bundle create help` writes into ~/.ctxloom/content/ and
// `agent default help` rewrites the user's real global config.yaml. Both
// happened, to a file with no git history and no backup.
//
// testsupport.Isolate does NOT cover this: it roots HOME (findAppDir's second
// route) and never touches cwd (the first). And no per-test helper can, because
// the hole is open for every test that forgets to call one. TestMain is the one
// seam that runs whether a test opts in or not — see testsupport.SandboxedMain,
// which also refuses to run any test at all if the sandbox did not take.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}

// TestCLITestBinary_IsSandboxed is the positive half: inside this binary,
// ctxloom's app-dir resolution cannot reach anything outside the temp root —
// with or without a per-test Isolate/ProjectDir call.
func TestCLITestBinary_IsSandboxed(t *testing.T) {
	testsupport.RequireIsolatedAppDir(t)
}

// TestCLITestBinary_FailsClosedWithoutTheSandbox is the negative half, and the
// point of the exercise: a guard nobody has driven red is not a guard.
//
// It re-runs THIS test binary with the sandbox disabled and pointed at
// non-temp directories — a real HOME and a working directory whose ancestry is
// the checkout itself, i.e. exactly the situation that wrote to the user's real
// ~/.ctxloom. The child must EXIT NONZERO with the guard's message before
// running anything. `-test.run=^$` means that even if the guard failed to fire,
// the child would still select no tests and write nothing.
func TestCLITestBinary_FailsClosedWithoutTheSandbox(t *testing.T) {
	unsandboxed := repoDir(t) // a real, non-temp directory tree

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = filepath.Join(unsandboxed, "internal", "cli")
	cmd.Env = append(os.Environ(),
		testsupport.SandboxOffEnv+"=1",
		"HOME="+unsandboxed,
		"USERPROFILE="+unsandboxed,
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("the sandbox guard did NOT go red with the sandbox disabled and HOME/cwd outside the temp root; output:\n%s", out)
	}
	if !strings.Contains(string(out), "REFUSING TO RUN TESTS") {
		t.Errorf("the child failed, but not with the guard's message — the failure proves nothing; output:\n%s", out)
	}
}

// pkgSourceDir returns this package's own source directory, and repoDir the
// module root, both derived from this file's compiled-in path rather than the
// working directory — TestMain has already moved that into the sandbox, which
// is the whole point. Every source-scanning test in this package (the exit-code
// policy sweep, the doc-comment sweep, the reach-back marker sweep) resolves
// through these: a scan rooted at "." silently scans an EMPTY directory once
// the binary is sandboxed, and a sweep that finds nothing reports "no
// violations", which is the false green these sweeps exist to prevent.
func pkgSourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: cannot locate this source file")
	}
	return filepath.Dir(file)
}

func repoDir(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(pkgSourceDir(t))) // internal/cli -> repo root
}
