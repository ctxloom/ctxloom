package taskstest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

// AppDirIsolationError reports why ctxloom's app-directory resolution could
// escape the OS temp root, or nil when it cannot. See appDirIsolationError.
//
// The body lives here rather than in internal/testsupport (which re-exports
// it) because Isolate — the helper this predicate guards — is here, and the
// internal/shared tree is self-contained: it must never import testsupport.
// See ChangeDir's doc for the same constraint stated the other way round.
func AppDirIsolationError() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve the home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot resolve the working directory: %w", err)
	}
	return appDirIsolationError(home, cwd, testTempRoots())
}

// appDirIsolationError is the pure predicate behind AppDirIsolationError, with
// the three inputs injected so it can be driven RED in a unit test without
// going anywhere near the real home (appdir_test.go).
//
// It mirrors the two routes config.findAppDir resolves by — deliberately as a
// STRICTER superset, not a second copy of that function:
//
//  1. the home fallback, ~/.ctxloom — so os.UserHomeDir() must be inside one
//     of tempRoots;
//  2. the walk UP FROM cwd, which findAppDir stops at os.TempDir() — so no
//     ancestor of cwd, up to that same boundary, may hold a .ctxloom that
//     lives outside every root in tempRoots.
//
// Anything findAppDir would resolve is therefore inside a recognized root
// whenever this returns nil. It never tries to predict WHICH directory
// findAppDir picks; asserting "not the user's real one" needs only the weaker
// containment fact, and a predictor would have to be kept in lockstep with
// findAppDir forever.
//
// tempRoots is a SLICE, not a single root, because home and cwd can be
// sandboxed under DIFFERENT roots at the same time: a per-test Isolate()
// mints a fresh HOME via t.TempDir() (which lands under GOTMPDIR when it is
// set — see testTempRoots), while the working directory is often still
// wherever the package-level testsupport.SandboxedMain left it, built via
// os.MkdirTemp("", ...) directly under os.TempDir(). Checking home and cwd
// against ANY recognized root, independently, is what makes both mechanisms
// valid at once instead of only whichever one happened to be tried first.
func appDirIsolationError(home, cwd string, tempRoots []string) error {
	roots := make([]string, len(tempRoots))
	for i, r := range tempRoots {
		roots[i] = realpath.Resolve(r)
	}

	if !underAnyRoot(home, roots) {
		return fmt.Errorf("HOME resolves to %q, outside every recognized temp root %v: the ~/.ctxloom fallback would hit the developer's real home", home, roots)
	}
	if esc, err := escapingAppDirAncestor(cwd, roots); err != nil {
		return err
	} else if esc != "" {
		return fmt.Errorf("the working directory %q has an ancestor app dir at %q, outside every recognized temp root %v: findAppDir's walk-up would adopt it as the project", cwd, esc, roots)
	}
	return nil
}

// escapingAppDirAncestor walks up from cwd exactly as config.findAppDir does —
// same AppDirName marker, same "stop at the OS temp root" boundary, now
// generalized to a set of boundaries — and returns the first app dir it finds
// that is NOT inside any of tempRoots.
func escapingAppDirAncestor(cwd string, tempRoots []string) (string, error) {
	dir := realpath.Resolve(cwd)
	if dir == "" {
		return "", errors.New("the working directory does not exist")
	}
	for !containsResolvedRoot(dir, tempRoots) {
		candidate := filepath.Join(dir, appDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !underAnyRoot(candidate, tempRoots) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

// containsResolvedRoot reports whether dir (already symlink-resolved) IS one
// of tempRoots (also symlink-resolved by the caller) — the walk's stopping
// condition, checked separately from underAnyRoot's "at or beneath" test
// because the walk must stop exactly AT a root, not merely once it is
// beneath one.
func containsResolvedRoot(dir string, tempRoots []string) bool {
	for _, root := range tempRoots {
		if dir == root {
			return true
		}
	}
	return false
}

// underAnyRoot reports whether path is at or beneath any of roots.
func underAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if realpath.Under(path, root) {
			return true
		}
	}
	return false
}

// appDirName duplicates paths.AppDirName rather than importing it: the shared
// tree is self-contained and cannot reach internal/paths, and this package is
// imported BY internal/config's own tests, so any edge into the config/paths
// tree also risks an import cycle.
const appDirName = ".ctxloom"

// testTempRoots duplicates operations.testTempRoots (see its doc for the
// mechanism) rather than importing it: this package must stay self-contained
// (see the realpath.Resolve / appDirName notes above for why), and
// internal/operations already imports this package's Isolate/ChangeDir for
// its own tests, so the reverse edge would cycle.
//
// In one sentence: os.TempDir() is what a TestMain-style sandbox
// (enterSandbox) mkdirs into directly, and GOTMPDIR — read directly by
// go1.26.8's testing.(*common).makeTempDir, bypassing os.TempDir() — is what
// t.TempDir() actually allocates under whenever it is set, which this
// project's justfile does on purpose. Both are live sandbox roots; neither
// alone is sufficient.
// UnderTestTempRoot reports whether path lives under a root a test may
// legitimately write to: os.TempDir(), or GOTMPDIR when it is set.
//
// Exported because the same question is asked outside this package. A test that
// isolates HOME and then PROVES the isolation took must compare against the
// same roots Isolate actually allocates under, and t.TempDir() lands under
// GOTMPDIR whenever it is set — so a guard that checks only os.TempDir() can
// never pass under this project's own gate, which sets GOTMPDIR deliberately.
// Answering it here keeps ONE predicate; a second copy is what drifted.
func UnderTestTempRoot(path string) bool {
	for _, r := range testTempRoots() {
		if realpath.Under(path, r) {
			return true
		}
	}
	return false
}

func testTempRoots() []string {
	roots := []string{os.TempDir()}
	if v := os.Getenv("GOTMPDIR"); v != "" {
		roots = append(roots, v)
	}
	return roots
}

// appDirReporter is the part of *testing.T that requireIsolatedAppDir needs.
// It is an interface for the same reason errorReporter is one: a check that
// can only report by failing the test that calls it cannot be asserted ON.
// With a recorder standing in for t, appdir_test.go can prove the check fires
// — and prove what it says — without the test itself going red.
type appDirReporter interface {
	Helper()
	Fatalf(format string, args ...any)
}

// requireIsolatedAppDir fails the test when ctxloom's app-directory resolution
// could still reach outside the OS temp root, UNLESS pkg is on
// appDirEscapeRatchet.
//
// This is what makes Isolate fail closed. Isolate roots HOME at a temp dir,
// which closes exactly one of config.findAppDir's routes; the other is the
// walk UP FROM the working directory, which Isolate never touches. A test
// binary's cwd is its own package source directory, so in an ordinary checkout
// the walk-up reaches the repository's own .ctxloom — or, on a machine where
// the checkout sits under $HOME, the developer's REAL ~/.ctxloom — and adopts
// it as the project app dir. That is not hypothetical: it is how a test run
// created ~/.ctxloom/content/ and wrote default_agent into a real global
// config.yaml, destroying the value that was there.
func requireIsolatedAppDir(t appDirReporter, pkg string) {
	t.Helper()
	err := AppDirIsolationError()
	if err == nil {
		return
	}
	if appDirEscapeRatchet[pkg] {
		return
	}
	t.Fatalf("taskstest: app-dir resolution is not isolated — %v\n"+
		"Isolate only roots HOME; the walk up from the working directory is closed by the "+
		"process-wide sandbox. Add it to package %s:\n"+
		"\tfunc TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }\n"+
		"If that package genuinely cannot adopt it yet, add %q to appDirEscapeRatchet in "+
		"internal/shared/tasks/taskstest/ratchet.go — that list may only shrink.",
		err, pkg, pkg)
}

// callerPackage names the package of the TEST that is calling Isolate, as a
// repository-relative import path ("internal/config"). See callerPackageFrom
// for the rule; this half only collects the stack.
func callerPackage() string {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var names []string
	for {
		frame, more := frames.Next()
		names = append(names, frame.Function)
		if !more {
			break
		}
	}
	return callerPackageFrom(names)
}

// callerPackageFrom picks the ratchet key out of a stack of runtime function
// names, innermost first.
//
// It keys on the TEST FUNCTION's frame — the one immediately inside testing's
// runner — and not on Isolate's immediate caller, because the escape being
// ratcheted is a property of the test BINARY, whose working directory is its
// own package's source directory. A shared fixture living in some other
// package must not launder every binary that calls it into a single ratchet
// entry, exempting packages nobody ever decided to exempt.
//
// Taking a slice rather than reading the stack itself is what makes that rule
// assertable: the interesting case is a stack whose innermost frames belong to
// a DIFFERENT package than the test, which no test can produce about itself.
func callerPackageFrom(names []string) string {
	prev, firstForeign := "", ""
	for _, name := range names {
		pkg := packageOfFunc(name)
		if pkg == testingPackage && prev != "" {
			return normalizePackage(prev)
		}
		if firstForeign == "" && pkg != "" && pkg != thisPackage && pkg != testsupportPackage {
			firstForeign = pkg
		}
		prev = pkg
	}
	// No testing frame: Isolate was reached from something other than a test
	// function body (a TestMain, or a goroutine). The innermost frame outside
	// the two isolation helpers is the best available answer.
	return normalizePackage(firstForeign)
}

// testingPackage is where the stack walk stops: testing.tRunner is the frame
// that called the test function.
const testingPackage = "testing"

// packageOfFunc extracts the import path from a runtime frame's function name
// ("github.com/ctxloom/ctxloom/internal/config.TestLoad.func1" ->
// "github.com/ctxloom/ctxloom/internal/config").
func packageOfFunc(name string) string {
	slash := strings.LastIndex(name, "/")
	dot := strings.Index(name[slash+1:], ".")
	if dot < 0 {
		return name
	}
	return name[:slash+1+dot]
}

// normalizePackage renders a full import path as the repository-relative key
// the ratchet is written in, folding Go's external test package
// ("internal/cli_test") into the package it tests.
func normalizePackage(full string) string {
	if full == "" {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(full, repoPackagePrefix), "_test")
}

// thisPackage and testsupportPackage are the two isolation helpers whose own
// frames callerPackage must look past.
var (
	thisPackage        = repoPackagePrefix + thisPackageRelative
	testsupportPackage = repoPackagePrefix + "internal/testsupport"
)

// thisPackageRelative is this package's repository-relative import path, used
// to recover the module prefix from a runtime frame rather than hard-coding
// it (a hard-coded module path silently stops matching after a rename, and a
// ratchet that matches nothing exempts nothing — or everything).
const thisPackageRelative = "internal/shared/tasks/taskstest"

var repoPackagePrefix = deriveRepoPackagePrefix()

func deriveRepoPackagePrefix() string {
	pc, _, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(packageOfFunc(runtime.FuncForPC(pc).Name()), thisPackageRelative)
}
