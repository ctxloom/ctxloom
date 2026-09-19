package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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
// still physically existed and confused worktree-safe WIP detection).
//
// testsupport.SandboxedMain moves the whole binary into a temp cwd (and a
// temp HOME) before any test runs, which also closes config.findAppDir's
// walk-up from the source directory. The guard afterward is what makes an
// escape fail loudly instead of leaving a nested .ctxloom for .gitignore to
// hide.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		origWD, wdErr := os.Getwd()

		code := testsupport.SandboxedMain(m)

		// A stray relative ".ctxloom" under the package source dir means a test
		// escaped isolation despite the sandbox (e.g. it used an absolute path
		// built from the pre-sandbox cwd).
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
