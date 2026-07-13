package operations

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/selfexec"
)

// TestMain isolates the package from the developer's real home directory AND
// from the package source directory as a working dir.
//
// HOME isolation: several operations fall back to the home config
// (config.HomeConfigDir consumers); without it, whatever
// profiles or remotes the developer has in ~/.ctxloom leak into unit tests and
// change collection counts and sync statuses. It also makes companion-binary
// detection deterministic (built-in bundle fragments/hooks/MCP inject only when
// ltk/taskloom are on PATH); tests opt back in via config.SetLookPathForTesting.
//
// CWD isolation: getBaseDir falls back to a RELATIVE ".ctxloom" when a Config
// carries no AppPaths, and getFS(nil) writes to the real OS filesystem — so a
// test reaching a trust/cache write without AppPaths (or testsupport.Isolate)
// would write ".ctxloom/..." into the package source dir (cwd during `go test`).
// We chdir into a throwaway dir so any such relative fallback is discarded, and
// GUARD the source dir afterward so a future regression fails loudly instead of
// silently re-appearing and getting committed. (`just test-dirty` junks HOME but
// not CWD — this closes that half.)
//
// realHOME is the ambient HOME this process started with, captured before
// TestMain ever overwrites it. Tests that shell out to `go` (e.g. to spawn
// this package's own test binary as a subprocess) need it: inheriting the
// sandboxed HOME instead would point the child's build phase at a throwaway
// GOPATH/GOCACHE, filling the sandbox with a full module-cache tree that
// os.RemoveAll then can't clean up, because the Go module cache marks its
// directories read-only.
var realHOME = os.Getenv("HOME")

// HOME and CWD each get their own subdirectory under a single per-process
// sandbox (see acquireSandbox), rather than two independent MkdirTemp calls,
// because a panic anywhere in m.Run() crashes the process via tRunner's
// re-panic before any defer here — including this one — ever runs. A single
// sandbox directory means the NEXT process's startup reaper only has one
// thing to find and remove per dead pid instead of two independent ones, and
// a signal (SIGINT/SIGTERM, unlike a panic, IS catchable) can tear down both
// halves with one RemoveAll instead of coordinating two.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		sandbox, cleanupSandbox := acquireSandbox()
		defer cleanupSandbox()

		// A panic crashes the process before this defer (or any other) runs,
		// so it cannot reap its own sandbox — only a LATER process's startup
		// reaper (acquireSandbox -> reapSandboxes) can, once this pid is dead.
		// SIGINT/SIGTERM, by contrast, are ordinary signals this process can
		// catch, so honor them by cleaning up before dying instead of leaving
		// that to the next reaper.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		go func() {
			sig, ok := <-sigCh
			if !ok {
				return
			}
			cleanupSandbox()
			os.Exit(128 + int(sig.(syscall.Signal))) //nolint:forbidigo // no *testing.T in TestMain
		}()

		home := filepath.Join(sandbox, "home")
		if err := os.MkdirAll(home, 0o700); err == nil {
			// TestMain has no *testing.T, so t.Setenv / testsupport.Isolate are
			// unavailable here; set the process env directly for the whole package run.
			os.Setenv("HOME", home) //nolint:forbidigo // no *testing.T in TestMain
		}

		// Belt and braces: internal/remote's runGit already forces every git
		// subprocess non-interactive (GIT_TERMINAL_PROMPT=0, cleared askpass), so
		// nothing in THIS package should ever reach a real credential prompt. Set
		// it here too, package-wide, so a future test that talks to a real (even
		// unreachable) repo URL fails fast on a network/auth error instead of
		// blocking forever or popping a GUI dialog — a hanging test is worse than
		// a failing one.
		os.Setenv("GIT_TERMINAL_PROMPT", "0") //nolint:forbidigo // no *testing.T in TestMain
		os.Setenv("GIT_ASKPASS", "")           //nolint:forbidigo // no *testing.T in TestMain
		os.Setenv("SSH_ASKPASS", "")           //nolint:forbidigo // no *testing.T in TestMain

		origWD, wdErr := os.Getwd()
		work := filepath.Join(sandbox, "cwd")
		if err := os.MkdirAll(work, 0o700); err == nil {
			if cerr := os.Chdir(work); cerr == nil { //nolint:forbidigo // no *testing.T in TestMain
				defer func() {
					if wdErr == nil {
						_ = os.Chdir(origWD) //nolint:forbidigo // no *testing.T in TestMain
					}
				}()
			}
		}

		restore := config.SetLookPathForTesting(func(string) (string, error) {
			return "", exec.ErrNotFound
		})
		defer restore()

		// Pin the self-exec command (agent.CtxloomCommand → selfexec.Path) to
		// the literal "ctxloom": in production the running binary IS named
		// ctxloom, so a materialized command's exec token always matches
		// agent.IsManaged's hardcoded "ctxloom" bin; under `go test`, Path's
		// natural answer is this test binary's own path, which would make
		// every managed-detection/removal test in this package see an
		// unrecognized command.
		restoreExe := selfexec.SetPathForTesting("ctxloom")
		defer restoreExe()

		code := m.Run()

		// A stray relative ".ctxloom" under the package source dir means a test
		// escaped isolation (wrote via the OS fs with no AppPaths, and the chdir
		// above didn't catch it — e.g. it failed, or the test used an absolute
		// source path). Clean it and fail so the leak can't be committed.
		if wdErr == nil {
			leak := filepath.Join(origWD, config.AppDirName)
			if _, statErr := os.Stat(leak); statErr == nil {
				_ = os.RemoveAll(leak)
				fmt.Fprintf(os.Stderr,
					"operations test isolation FAILED: a test wrote %s into the package source dir "+
						"(missing AppPaths / testsupport.Isolate; see getBaseDir + getFS(nil))\n", leak)
				if code == 0 {
					code = 1
				}
			}
		}
		return code
	}())
}
