package operations

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/lm/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// realHOME is the ambient HOME this process started with, captured before
// TestMain ever overwrites it. A test that must prove nothing reached the
// developer's genuine ~/.ctxloom needs the pre-sandbox location to inspect.
var realHOME = os.Getenv("HOME")

// TestMain sandboxes the WHOLE package binary — an isolated HOME and an
// isolated working directory, installed before a single test runs — via
// testsupport.SandboxedMain, and pins the package-specific seams around it.
//
// HOME isolation: several operations fall back to the home config
// (paths.HomeConfigDir consumers); without it, whatever profiles or remotes
// the developer has in ~/.ctxloom leak into unit tests and change collection
// counts and sync statuses.
//
// CWD isolation: getBaseDir falls back to a RELATIVE ".ctxloom" when a Config
// carries no AppPaths, and getFS(nil) writes to the real OS filesystem — so a
// test reaching a trust/cache write without AppPaths (or testsupport.Isolate)
// would write ".ctxloom/..." into the package source dir (cwd during `go
// test`). The sandbox's chdir discards any such relative fallback.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(func() int {
		// Belt and braces: internal/remote's runGit already forces every git
		// subprocess non-interactive (GIT_TERMINAL_PROMPT=0, cleared askpass), so
		// nothing in THIS package should ever reach a real credential prompt. Set
		// it here too, package-wide, so a future test that talks to a real (even
		// unreachable) repo URL fails fast on a network/auth error instead of
		// blocking forever or popping a GUI dialog — a hanging test is worse than
		// a failing one.
		os.Setenv("GIT_TERMINAL_PROMPT", "0") //nolint:forbidigo // no *testing.T in TestMain
		os.Setenv("GIT_ASKPASS", "")          //nolint:forbidigo // no *testing.T in TestMain
		os.Setenv("SSH_ASKPASS", "")          //nolint:forbidigo // no *testing.T in TestMain

		// Companion-binary detection must be deterministic (built-in bundle
		// fragments/hooks/MCP inject only when ltk/taskloom are on PATH); tests
		// opt back in via config.SetLookPathForTesting.
		restore := config.SetLookPathForTesting(func(string) (string, error) {
			return "", exec.ErrNotFound
		})
		defer restore()

		// testBaseDir is a SYNTHETIC absolute app dir that only ever exists in
		// an injected memfs — except that an operation reached with no fs
		// seam falls back to getFS(nil), the real OS filesystem, and a process
		// with the rights to create the parent (root in a container) then
		// materializes it for real. Nothing in the sandbox can catch that: it
		// is an absolute path, so neither the HOME nor the cwd redirect is
		// consulted. Once it exists, every later test that reads the OS fs at
		// testBaseDir sees the leaked fixtures, and the package goes red only
		// on the SECOND run. Guard it here: if the parent was absent before
		// the run and present after, a test leaked; clean it and fail loudly
		// so the leak cannot be committed.
		syntheticRoot := filepath.Dir(testBaseDir)
		_, preErr := os.Stat(syntheticRoot)

		code := testsupport.SandboxedMain(m)

		if _, err := os.Stat(syntheticRoot); os.IsNotExist(preErr) && err == nil {
			_ = os.RemoveAll(syntheticRoot)
			fmt.Fprintf(os.Stderr,
				"operations test isolation FAILED: a test wrote %s onto the real filesystem "+
					"(an operation reached getFS(nil) with a synthetic testBaseDir; inject the memfs)\n", syntheticRoot)
			if code == 0 {
				code = 1
			}
		}
		return code
	}())
}
