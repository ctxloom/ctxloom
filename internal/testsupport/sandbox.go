package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// SandboxOffEnv disables SandboxedMain's process-wide sandbox. It exists for
// ONE caller: the self-test that proves the guard below can go red (see
// internal/cli's TestCLITestBinary_FailsClosedWithoutTheSandbox). Nothing in a
// normal test run may set it — with the sandbox off, SandboxedMain refuses to
// run any test at all rather than letting the binary loose on the real home.
const SandboxOffEnv = "CTXLOOM_TEST_SANDBOX_OFF"

// sandboxRootEnv marks "this process is already inside a test sandbox", so a
// re-exec'd child adopts the parent's rather than minting its own. See
// SandboxedMain for why it sits outside the CTXLOOM_* namespace.
const sandboxRootEnv = "GOTEST_CTXLOOM_SANDBOX_ROOT"

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
	// sandboxRootEnv already set: this process was spawned BY a sandboxed test
	// (internal/cli has commands that re-exec os.Executable(), which under
	// test is the test binary itself). It inherited that sandbox's HOME and
	// cwd, so it is already isolated — minting a second one would only leave a
	// directory behind, since such a child is routinely killed by its parent's
	// context before any deferred cleanup could run.
	//
	// The variable is deliberately NOT in the CTXLOOM_* namespace: EnvKeys
	// covers that namespace and Isolate clears every key in it, which would
	// hide the parent's sandbox from any child spawned after the first
	// Isolate call — the exact case this exists to handle.
	if os.Getenv(SandboxOffEnv) == "" && os.Getenv(sandboxRootEnv) == "" {
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
// escape the OS temp root, or nil when it cannot.
//
// The predicate itself lives in taskstest, next to the Isolate it now guards:
// the internal/shared tree is self-contained and cannot import testsupport,
// so a shared-side caller forces the body shared-side. This is a re-export,
// not a copy — two bodies is how the two EnvKeys lists drifted to cover 3 of
// ~18 variables with nothing to catch it.
func AppDirIsolationError() error {
	return taskstest.AppDirIsolationError()
}

// enterSandbox roots HOME and the working directory at fresh temp directories
// and clears every EnvKeys variable, process-wide (os.Setenv, not t.Setenv:
// there is no *testing.T at TestMain time). The returned func restores the
// working directory and removes the sandbox.
func enterSandbox() (func(), error) {
	realHome, err := os.UserHomeDir() // BEFORE the redirect below
	if err != nil {
		return nil, err
	}
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
	if err := pinGoToolchainDirs(realHome); err != nil {
		return nil, err
	}
	if err := os.Setenv(sandboxRootEnv, root); err != nil {
		return nil, err
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
		_ = os.Unsetenv(sandboxRootEnv)
		removeAllForced(root)
	}, nil
}

// pinGoToolchainDirs resolves the Go toolchain's HOME-derived directories to
// their pre-sandbox values and sets them explicitly, so redirecting HOME does
// not also redirect them.
//
// HOME is overloaded: it is where ctxloom finds ~/.ctxloom AND where the go
// command finds its module cache, build cache and env file. A test that shells
// out to `go build` (internal/cli's MCP wire-protocol test does) inherits the
// sandbox HOME, so without this the toolchain treats every run as a cold
// machine and re-downloads the entire module cache into the sandbox —
// measured at 596 MB, per run, over the network. It also makes the sandbox
// unremovable, because the module cache is deliberately read-only.
//
// Nothing here weakens the isolation: these name Go's caches, not ctxloom's
// app dir, and AppDirIsolationError still governs everything findAppDir
// consults. An explicitly-set value always wins — this only supplies the
// default the toolchain would have derived from HOME itself.
func pinGoToolchainDirs(realHome string) error {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(realHome, ".cache")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(realHome, ".config")
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(realHome, "go")
	}
	for k, v := range map[string]string{
		"GOPATH":     gopath,
		"GOMODCACHE": filepath.Join(gopath, "pkg", "mod"),
		"GOCACHE":    filepath.Join(cacheHome, "go-build"),
		"GOENV":      filepath.Join(configHome, "go", "env"),
	} {
		if os.Getenv(k) != "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// removeAllForced deletes root, retrying once with write permission restored
// on every directory beneath it. A plain RemoveAll gives up on a read-only
// tree, and leaving multi-hundred-megabyte sandboxes behind in /tmp on every
// test run is its own kind of damage.
func removeAllForced(root string) {
	if err := os.RemoveAll(root); err == nil {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}
