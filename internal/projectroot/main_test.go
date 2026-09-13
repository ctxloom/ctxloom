package projectroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain closes config.findAppDir's walk-up from the working directory for
// every test in this binary, not only the ones that remember to isolate. A
// temp HOME alone does not: see testsupport.SandboxedMain.
func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }

// tempGitRepo chdirs into a fresh temp project dir and gives it the minimal
// .git skeleton gitutil.FindRoot accepts, returning the repo root.
//
// The tests that need "the cwd is inside a repository" used to get it from the
// SUITE running inside the ctxloom checkout. TestMain now moves every test into
// a sandbox cwd, so that no longer holds — and it was never sound: it made the
// assertion a function of where the suite happened to be run from, which is
// exactly the ambient coupling the app-dir sandbox exists to close.
func tempGitRepo(t *testing.T) string {
	t.Helper()
	repo := testsupport.ProjectDir(t)
	for _, d := range []string{"objects", "refs"} {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "HEAD"),
		[]byte("ref: refs/heads/main\n"), 0o644))
	return repo
}
