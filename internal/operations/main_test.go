package operations

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/engines"
	"github.com/ctxloom/ctxloom/internal/selfexec"
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
// (config.HomeConfigDir consumers); without it, whatever profiles or remotes
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

		// Pin the self-exec command (agent.CtxloomCommand → selfexec.Path) to
		// the literal "ctxloom": in production the running binary IS named
		// ctxloom, so a materialized command's exec token always matches
		// agent.IsManaged's hardcoded "ctxloom" bin; under `go test`, Path's
		// natural answer is this test binary's own path, which would make
		// every managed-detection/removal test in this package see an
		// unrecognized command.
		restoreExe := selfexec.SetPathForTesting("ctxloom")
		defer restoreExe()

		return testsupport.SandboxedMain(m)
	}())
}
