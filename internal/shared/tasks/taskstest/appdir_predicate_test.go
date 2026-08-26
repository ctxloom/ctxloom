package taskstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard has to be drivable RED, or it is decoration. appDirIsolationError
// takes its three inputs as arguments precisely so both escape routes can be
// exercised against directories this test owns — a stand-in "temp root" nested
// inside the real one, so "outside the temp root" is expressible without
// touching anybody's real home.
func TestAppDirIsolationError_CatchesBothEscapeRoutes(t *testing.T) {
	base := t.TempDir()
	// tempRoot is the pretend OS temp root; anything in base but outside it is
	// "the real world" as far as the predicate is concerned.
	tempRoot := filepath.Join(base, "tmp")
	sandboxHome := filepath.Join(tempRoot, "home")
	sandboxCwd := filepath.Join(tempRoot, "work")
	realHome := filepath.Join(base, "realhome")
	for _, d := range []string{sandboxHome, sandboxCwd, realHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("isolated home and cwd pass", func(t *testing.T) {
		if err := appDirIsolationError(sandboxHome, sandboxCwd, tempRoot); err != nil {
			t.Fatalf("a home and cwd both inside the temp root must be accepted: %v", err)
		}
	})

	// Route 1: findAppDir's ~/.ctxloom home fallback. This is the escape
	// setupEditProject had — chdir into a t.TempDir but never isolate HOME.
	t.Run("unisolated HOME is red", func(t *testing.T) {
		err := appDirIsolationError(realHome, sandboxCwd, tempRoot)
		if err == nil {
			t.Fatal("a HOME outside the temp root must be reported: the ~/.ctxloom fallback reaches the real home")
		}
		if !strings.Contains(err.Error(), realHome) {
			t.Errorf("the message must name the offending home; got %v", err)
		}
	})

	// Route 2: the walk up from cwd. This is the escape testsupport.Isolate
	// alone leaves open — HOME is a temp dir, but the checkout sits under a
	// directory that has a real .ctxloom, and findAppDir's walk-up finds it
	// first. Isolate never touches cwd, so it cannot see this coming.
	t.Run("a .ctxloom above an unisolated cwd is red", func(t *testing.T) {
		project := filepath.Join(base, "checkout")
		if err := os.MkdirAll(filepath.Join(project, appDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		deep := filepath.Join(project, "internal", "cli")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}

		err := appDirIsolationError(sandboxHome, deep, tempRoot)
		if err == nil {
			t.Fatal("an ancestor .ctxloom outside the temp root must be reported: findAppDir's walk-up adopts it as the project")
		}
		if !strings.Contains(err.Error(), filepath.Join(project, appDirName)) {
			t.Errorf("the message must name the ancestor app dir it would have adopted; got %v", err)
		}
	})

	// The boundary findAppDir itself honours: a .ctxloom INSIDE the temp root
	// is an ordinary test fixture, not an escape.
	t.Run("a .ctxloom inside the temp root is fine", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(sandboxCwd, appDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := appDirIsolationError(sandboxHome, sandboxCwd, tempRoot); err != nil {
			t.Fatalf("a fixture app dir inside the temp root must be accepted: %v", err)
		}
	})
}
