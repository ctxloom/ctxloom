package taskstest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	return appDirIsolationError(home, cwd, os.TempDir())
}

// appDirIsolationError is the pure predicate behind AppDirIsolationError, with
// the three inputs injected so it can be driven RED in a unit test without
// going anywhere near the real home (appdir_test.go).
//
// It mirrors the two routes config.findAppDir resolves by — deliberately as a
// STRICTER superset, not a second copy of that function:
//
//  1. the home fallback, ~/.ctxloom — so os.UserHomeDir() must be inside
//     tempRoot;
//  2. the walk UP FROM cwd, which findAppDir stops at os.TempDir() — so no
//     ancestor of cwd, up to that same boundary, may hold a .ctxloom that
//     lives outside tempRoot.
//
// Anything findAppDir would resolve is therefore inside tempRoot whenever this
// returns nil. It never tries to predict WHICH directory findAppDir picks;
// asserting "not the user's real one" needs only the weaker containment fact,
// and a predictor would have to be kept in lockstep with findAppDir forever.
func appDirIsolationError(home, cwd, tempRoot string) error {
	tempRoot = resolveRealPath(tempRoot)

	if !underRoot(home, tempRoot) {
		return fmt.Errorf("HOME resolves to %q, outside the temp root %q: the ~/.ctxloom fallback would hit the developer's real home", home, tempRoot)
	}
	if esc, err := escapingAppDirAncestor(cwd, tempRoot); err != nil {
		return err
	} else if esc != "" {
		return fmt.Errorf("the working directory %q has an ancestor app dir at %q, outside the temp root %q: findAppDir's walk-up would adopt it as the project", cwd, esc, tempRoot)
	}
	return nil
}

// escapingAppDirAncestor walks up from cwd exactly as config.findAppDir does —
// same AppDirName marker, same "stop at the OS temp root" boundary — and
// returns the first app dir it finds that is NOT inside tempRoot.
func escapingAppDirAncestor(cwd, tempRoot string) (string, error) {
	dir := resolveRealPath(cwd)
	if dir == "" {
		return "", errors.New("the working directory does not exist")
	}
	tempRoot = resolveRealPath(tempRoot)
	for dir != tempRoot {
		candidate := filepath.Join(dir, appDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !underRoot(candidate, tempRoot) {
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

// appDirName duplicates paths.AppDirName rather than importing it: the shared
// tree is self-contained and cannot reach internal/paths, and this package is
// imported BY internal/config's own tests, so any edge into the config/paths
// tree also risks an import cycle.
const appDirName = ".ctxloom"

// underRoot reports whether path is root itself or lives beneath it, comparing
// symlink-resolved paths (the OS temp root is a symlink on macOS, and t.TempDir
// hands back the unresolved form).
func underRoot(path, root string) bool {
	path, root = resolveRealPath(path), resolveRealPath(root)
	if path == "" || root == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolveRealPath returns p symlink-resolved and cleaned, falling back to the
// cleaned absolute form when the path does not exist (a nonexistent path still
// has to compare sensibly against a root).
func resolveRealPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
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
