package testsupport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SandboxOffEnv disables SandboxedMain's process-wide sandbox. It exists for
// ONE caller: the self-test that proves the guard below can go red (see
// internal/cli's TestCLITestBinary_FailsClosedWithoutTheSandbox). Nothing in a
// normal test run may set it — with the sandbox off, SandboxedMain refuses to
// run any test at all rather than letting the binary loose on the real home.
const SandboxOffEnv = "CTXLOOM_TEST_SANDBOX_OFF"

// SandboxedMain is the TestMain body for any package whose tests drive code
// that resolves ctxloom's app directory (config.Load / cli.GetConfig and every
// operation reached through them). Use it as:
//
//	func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }
//
// WHY A TestMain AND NOT JUST Isolate. Isolate roots HOME at a temp dir, which
// covers exactly ONE of config.findAppDir's two resolution routes. The other
// is the walk UP FROM THE WORKING DIRECTORY, which Isolate does not touch: a
// test binary runs with cwd = its own package source directory, so on any
// machine where the checkout sits beneath a directory that has a real
// .ctxloom (a developer's ~/workspace/... under $HOME is the ordinary case)
// the walk-up finds the USER'S OWN ~/.ctxloom and adopts it as the project app
// dir — HOME isolation is simply bypassed. That is not hypothetical: it is how
// a `cmd.RunE` driven from internal/cli's tests created ~/.ctxloom/content/
// and wrote default_agent into the user's real global config.yaml.
//
// A per-test helper cannot close that hole, because the hole is open for tests
// that never call the helper. Moving the whole binary into a temp cwd before
// any test runs does close it: findAppDir's walk-up stops at os.TempDir(), so
// a cwd under the temp root can never reach an ancestor outside it, and the
// home fallback then lands in the temp HOME this function also installs.
//
// FAIL CLOSED. Before running a single test, SandboxedMain re-derives the
// isolation from the environment it just installed (AppDirIsolationError) and
// returns a nonzero exit with a loud message if it does not hold. A guard that
// silently does nothing when it is bypassed is worth less than no guard.
func SandboxedMain(m *testing.M) int {
	if os.Getenv(SandboxOffEnv) == "" {
		cleanup, err := enterSandbox()
		if err != nil {
			fmt.Fprintf(os.Stderr, "test sandbox: could not establish an isolated HOME/cwd: %v\n", err)
			return 1
		}
		defer cleanup()
	}

	if err := AppDirIsolationError(); err != nil {
		fmt.Fprintf(os.Stderr, "test sandbox: REFUSING TO RUN TESTS — %v\n"+
			"These tests drive code that resolves ctxloom's app directory; running them "+
			"un-sandboxed writes into the developer's real ~/.ctxloom.\n", err)
		return 1
	}

	return m.Run()
}

// RequireIsolatedAppDir fails the calling test if ctxloom's app-directory
// resolution could reach anything outside the OS temp root. It is the per-test
// form of SandboxedMain's startup check, for a test that moves HOME or the
// working directory itself and wants to prove it stayed inside the sandbox.
func RequireIsolatedAppDir(t *testing.T) {
	t.Helper()
	if err := AppDirIsolationError(); err != nil {
		t.Fatalf("app-dir resolution is not isolated: %v", err)
	}
}

// AppDirIsolationError reports why ctxloom's app-directory resolution could
// escape the OS temp root, or nil when it cannot. See appDirIsolationError.
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
// going anywhere near the real home (sandbox_test.go).
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
	tempRoot = resolvePath(tempRoot)

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
	dir := resolvePath(cwd)
	if dir == "" {
		return "", errors.New("the working directory does not exist")
	}
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

// appDirName duplicates paths.AppDirName rather than importing it: this
// package is imported BY internal/config's own tests (package config), so any
// edge from testsupport into the config/paths tree risks an import cycle.
const appDirName = ".ctxloom"

// enterSandbox roots HOME and the working directory at fresh temp directories
// and clears every EnvKeys variable, process-wide (os.Setenv, not t.Setenv:
// there is no *testing.T at TestMain time). The returned func restores the
// working directory and removes the sandbox.
func enterSandbox() (func(), error) {
	root, err := os.MkdirTemp("", "ctxloom-test-sandbox-")
	if err != nil {
		return nil, err
	}
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, dir := range []string{home, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.Setenv("HOME", home); err != nil {
		return nil, err
	}
	if err := os.Setenv("USERPROFILE", home); err != nil { // Windows home, for os.UserHomeDir parity
		return nil, err
	}
	for _, k := range EnvKeys {
		if err := os.Unsetenv(k); err != nil {
			return nil, err
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(work); err != nil {
		return nil, err
	}
	return func() {
		_ = os.Chdir(prev)
		_ = os.RemoveAll(root)
	}, nil
}

// underRoot reports whether path is root itself or lives beneath it, comparing
// symlink-resolved paths (the OS temp root is a symlink on macOS, and t.TempDir
// hands back the unresolved form).
func underRoot(path, root string) bool {
	path, root = resolvePath(path), resolvePath(root)
	if path == "" || root == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolvePath returns p symlink-resolved and cleaned, falling back to the
// cleaned absolute form when the path does not exist (a nonexistent path still
// has to compare sensibly against a root).
func resolvePath(p string) string {
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
