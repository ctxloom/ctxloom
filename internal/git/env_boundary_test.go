package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every git child process this layer spawns must take its environment from
// gitutil.SanitizedEnviron, never from a raw os.Environ(). GIT_DIR and its
// siblings override which repository git operates on and IGNORE cmd.Dir
// entirely, so inheriting them verbatim means a caller that carefully set
// cmd.Dir can still be operating on someone else's repository — silently, and
// with a successful exit code.
//
// This is a structural pin rather than a behavioural one because the property
// belongs to the SPAWN SITE, not to any one command's output: a new git
// wrapper added tomorrow with its own exec.Command is exactly the regression
// worth catching, and it would pass every functional test in this package.
func TestGitSpawns_TakeASanitizedEnvironment(t *testing.T) {
	// Resolved from this test's compiled-in source path: a scan rooted at the
	// working directory finds nothing as soon as anything moves the cwd, and a
	// scan that finds nothing passes without ever looking at the code.
	_, thisFile, ok := callerFile()
	require.True(t, ok, "could not locate this test's own source file")
	pkgDir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(pkgDir)
	require.NoError(t, err)

	scanned, spawns := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(pkgDir, name))
		require.NoError(t, err)
		scanned++
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(trimmed, "exec.Command") {
				spawns++
			}
			if strings.Contains(trimmed, "cmd.Env =") && !strings.Contains(trimmed, "gitutil.SanitizedEnviron()") {
				t.Errorf("%s: %q builds a git child's environment without gitutil.SanitizedEnviron; "+
					"GIT_DIR and friends override cmd.Dir and would silently retarget the repository", name, trimmed)
			}
		}
	}

	// The scan is worthless if it matched nothing: prove it walked real files
	// and found the spawn site it exists to guard.
	require.NotZero(t, scanned, "the scan read no source files; it is not scanning what it thinks")
	require.NotZero(t, spawns, "the scan found no exec.Command site; it is not scanning what it thinks")
}

func callerFile() (uintptr, string, bool) {
	pc, file, _, ok := runtime.Caller(1)
	return pc, file, ok
}
