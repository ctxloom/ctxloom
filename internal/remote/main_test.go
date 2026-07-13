package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestMain isolates the package from the working directory. NewRegistry("")
// (and other callers threading paths.DefaultRemotesPath/paths.DefaultAppDir
// through with the real OS filesystem) default to a RELATIVE ".ctxloom" path
// resolved against the process's cwd — which, absent isolation, is this
// package's own source directory during `go test`. A test that forgets to
// chdir (testsupport.ProjectDir) or inject an in-memory fs (WithRegistryFS)
// would then write ".ctxloom/remotes.yaml" straight into internal/remote/,
// which is exactly what was found sitting there uncommitted (gitignored by
// internal/**/.ctxloom/, so `git status` never surfaced it, but the directory
// still physically existed and confused worktree-safe WIP detection). This
// mirrors internal/operations/main_test.go's guard: chdir into a throwaway
// dir before any test runs, then fail loudly if a nested .ctxloom still
// turns up afterward instead of silently leaving it for .gitignore to hide.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		origWD, wdErr := os.Getwd()
		if work, werr := os.MkdirTemp("", "ctxloom-remote-test-cwd-*"); werr == nil {
			if cerr := os.Chdir(work); cerr == nil { //nolint:forbidigo // no *testing.T in TestMain
				defer func() {
					if wdErr == nil {
						_ = os.Chdir(origWD) //nolint:forbidigo // no *testing.T in TestMain
					}
					_ = os.RemoveAll(work)
				}()
			}
		}

		code := m.Run()

		// A stray relative ".ctxloom" under the package source dir means a test
		// escaped isolation despite the chdir above (e.g. it used an absolute
		// path built from the pre-chdir cwd, or the chdir itself failed).
		if wdErr == nil {
			leak := filepath.Join(origWD, paths.AppDirName)
			if _, statErr := os.Stat(leak); statErr == nil {
				_ = os.RemoveAll(leak)
				fmt.Fprintf(os.Stderr,
					"remote test isolation FAILED: a test wrote %s into the package source dir "+
						"(missing WithRegistryFS / testsupport.ProjectDir)\n", leak)
				if code == 0 {
					code = 1
				}
			}
		}
		return code
	}())
}
