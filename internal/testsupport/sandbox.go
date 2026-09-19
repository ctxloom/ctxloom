package testsupport

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// SandboxOffEnv disables SandboxedMain's process-wide sandbox. It exists for
// ONE caller: the self-test that proves the guard below can go red (see
// internal/adapters/cli's TestCLITestBinary_FailsClosedWithoutTheSandbox). Nothing in a
// normal test run may set it — with the sandbox off, SandboxedMain refuses to
// run any test at all rather than letting the binary loose on the real home.
const SandboxOffEnv = "CTXLOOM_TEST_SANDBOX_OFF"

// SandboxRootEnv marks "this process is already inside a test sandbox", so a
// re-exec'd child adopts the parent's rather than minting its own. See
// SandboxedMain for why it sits outside the CTXLOOM_* namespace.
//
// It is exported for the one kind of test that wants the OPPOSITE: a test
// that spawns a fresh sandboxed process to observe SandboxedMain itself must
// strip this variable from the child's environment, or the child inherits the
// parent's sandbox and there is nothing to observe.
const SandboxRootEnv = "GOTEST_CTXLOOM_SANDBOX_ROOT"

// SandboxRootName is the directory under os.TempDir() that groups every
// sandboxed test process's sandbox by pid. One process = one pid = one
// TestMain invocation, so a single subdirectory per pid holds both the HOME
// and the working-directory halves, and a single RemoveAll of the pid
// directory tears both down together. Exported so a test that spawns a
// sandboxed child under its own TMPDIR can find what the child left behind.
const SandboxRootName = "ctxloom-test-sandbox"

// maxOrphanAge is how long a sandbox directory may sit unreaped before it is
// reclaimed regardless of whether its pid currently resolves to a live
// process. No real test run takes anywhere near this long; it exists only to
// bound the rare case where a dead run's pid gets recycled by an unrelated
// process before the liveness-only reap gets a chance to run.
const maxOrphanAge = 2 * time.Hour

// SandboxedMain is the TestMain body for any package whose tests drive code
// that resolves ctxloom's app directory (the configload reader, cli.GetConfig
// and every operation reached through them). Use it as:
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
// a `cmd.RunE` driven from internal/adapters/cli's tests created ~/.ctxloom/content/
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
	// (internal/adapters/cli has commands that re-exec os.Executable(), which under
	// test is the test binary itself). It inherited that sandbox's HOME and
	// cwd, so it is already isolated — minting a second one would only leave a
	// directory behind, since such a child is routinely killed by its parent's
	// context before any deferred cleanup could run.
	//
	// The variable is deliberately NOT in the CTXLOOM_* namespace: EnvKeys
	// covers that namespace and Isolate clears every key in it, which would
	// hide the parent's sandbox from any child spawned after the first
	// Isolate call — the exact case this exists to handle.
	if os.Getenv(SandboxOffEnv) == "" && os.Getenv(SandboxRootEnv) == "" {
		cleanup, err := enterSandbox()
		if err != nil {
			fmt.Fprintf(os.Stderr, "test sandbox: could not establish an isolated HOME/cwd: %v\n", err)
			return 1
		}
		defer cleanup()

		// A panic crashes the process before this defer (or any other) runs,
		// so it cannot reap its own sandbox — only a LATER process's startup
		// reaper (acquireSandbox -> reapSandboxes) can, once this pid is dead.
		// SIGINT/SIGTERM, by contrast, are ordinary signals this process can
		// catch, so honor them by cleaning up before dying instead of leaving
		// that to the next reaper.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		go func() {
			sig, ok := <-sigCh
			if !ok {
				return
			}
			cleanup()
			os.Exit(128 + int(sig.(syscall.Signal))) //nolint:forbidigo // no *testing.T in TestMain
		}()
	}

	// A `go test` binary gets no ldflags, so internal/shared/version.Version is empty
	// and every ctxloom command this binary drives would refuse to start over
	// its missing stamp. Stamp it here, in the one seam that runs whether a
	// test opts in or not, for the same reason the sandbox is installed here:
	// a per-test helper cannot cover the tests that forget to call it.
	StampTestBinary()

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
//
// HOME and cwd each get their own subdirectory under a single per-process
// sandbox (see acquireSandbox), rather than two independent MkdirTemp calls,
// because a panic anywhere in m.Run() crashes the process via tRunner's
// re-panic before any defer in SandboxedMain ever runs. A single sandbox
// directory means the NEXT process's startup reaper only has one thing to
// find and remove per dead pid instead of two independent ones, and a signal
// can tear down both halves with one RemoveAll instead of coordinating two.
func enterSandbox() (func(), error) {
	realHome, err := os.UserHomeDir() // BEFORE the redirect below
	if err != nil {
		return nil, err
	}
	root, removeSandbox, err := acquireSandbox()
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
	if err := os.Setenv(SandboxRootEnv, root); err != nil {
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
		_ = os.Unsetenv(SandboxRootEnv)
		removeSandbox()
	}, nil
}

// acquireSandbox reaps whatever dead runs left behind, then stakes out this
// process's own pid-scoped sandbox directory. The returned cleanup removes
// the whole directory; on the normal exit path that single deferred call is
// enough. On a panic, tRunner's re-panic tears down the process before any
// defer runs, so cleanup never fires — the NEXT process to call
// acquireSandbox reaps it instead via the pid-liveness check in
// reapSandboxes, since a panicked process's pid is dead the instant the
// process exits.
func acquireSandbox() (dir string, cleanup func(), err error) {
	root := filepath.Join(os.TempDir(), SandboxRootName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, err
	}
	reapSandboxes(root)

	dir = filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	return dir, func() { removeAllForced(dir) }, nil
}

// reapSandboxes removes sandbox directories left behind by processes that can
// no longer be running, without ever touching a directory that a CONCURRENT
// sibling process might still be using — `go test ./...` and mutation runners
// both keep several test binaries alive at once, so this must be provably
// safe under that concurrency.
//
// A directory is only removed when its owning pid is verifiably dead (an
// ESRCH-equivalent signal-0 probe, not a heuristic), or when it is older than
// maxOrphanAge regardless of pid liveness. The age fallback exists because a
// dead pid can be recycled by an unrelated, currently-alive process, which
// would otherwise make an orphaned directory look permanently "live" and
// never get reclaimed; recycling within maxOrphanAge is implausible on any
// machine this runs on. Neither path can misfire against a live sibling: a
// pid that is genuinely still running never satisfies either condition
// within the time it takes that sibling's own test run to finish, and a
// signal-0 probe cannot report "dead" for a process that is in fact alive.
//
// A directory named after THIS process's own pid is a special case: since
// this call always runs before this process creates its own directory, any
// existing entry under our pid cannot be ours — it is a leftover from
// whichever earlier process last held this (now recycled) pid, and that
// process is, by definition, not us and not running as this pid anymore, so
// it is always safe to remove.
func reapSandboxes(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return // nothing to reap yet (root doesn't exist) or unreadable; best-effort
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not one of our pid-named directories; leave it alone
		}
		path := filepath.Join(root, e.Name())
		if pid == self {
			removeAllForced(path)
			continue
		}
		info, statErr := e.Info()
		orphanedByAge := statErr == nil && time.Since(info.ModTime()) > maxOrphanAge
		// !MaybeAlive() (true only for a confirmed Dead) instead of a bare
		// !Alive check — removal is destructive, so an unconfirmable probe
		// must be left alone exactly like a confirmed-live pid, unless the
		// age fallback already independently justifies reaping it.
		if !pidalive.Probe(pid).MaybeAlive() || orphanedByAge {
			removeAllForced(path)
		}
	}
}

// pinGoToolchainDirs resolves the Go toolchain's HOME-derived directories to
// their pre-sandbox values and sets them explicitly, so redirecting HOME does
// not also redirect them.
//
// HOME is overloaded: it is where ctxloom finds ~/.ctxloom AND where the go
// command finds its module cache, build cache and env file. A test that shells
// out to `go build` (internal/adapters/cli's MCP wire-protocol test does) inherits the
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
