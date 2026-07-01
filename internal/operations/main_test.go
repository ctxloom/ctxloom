package operations

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestMain isolates the package from the developer's real home directory AND
// from the package source directory as a working dir.
//
// HOME isolation: several operations fall back to the home config
// (homeDefaultProfiles, config.HomeConfigDir consumers); without it, whatever
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
func TestMain(m *testing.M) {
	os.Exit(func() int {
		home, err := os.MkdirTemp("", "ctxloom-test-home-*")
		if err == nil {
			// TestMain has no *testing.T, so t.Setenv / testsupport.Isolate are
			// unavailable here; set the process env directly for the whole package run.
			os.Setenv("HOME", home) //nolint:forbidigo // no *testing.T in TestMain
			defer os.RemoveAll(home)
		}

		origWD, wdErr := os.Getwd()
		if work, werr := os.MkdirTemp("", "ctxloom-test-cwd-*"); werr == nil {
			if cerr := os.Chdir(work); cerr == nil {
				defer func() {
					if wdErr == nil {
						_ = os.Chdir(origWD)
					}
					_ = os.RemoveAll(work)
				}()
			}
		}

		restore := config.SetLookPathForTesting(func(string) (string, error) {
			return "", exec.ErrNotFound
		})
		defer restore()

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
