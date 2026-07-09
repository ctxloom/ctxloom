// Package taskstest provides shared isolation helpers so tests never read or
// write host or session state — neither the process environment, the user's
// ~/.ctxloom home (resolved via os.UserHomeDir), nor the working directory.
//
// A test inheriting the ambient session's environment is non-deterministic:
// e.g. CTXLOOM_PROJECT_ID selects the live task log, so an un-isolated task
// test reads the running session's tasks instead of its own.
package taskstest

import (
	"os"
	"testing"
)

// EnvKeys is the set of host/session environment variables the task store
// reads. Isolate clears each so a test inherits none of the ambient
// session's values.
var EnvKeys = []string{
	"CTXLOOM_SESSION_HARP",
	"CTXLOOM_PROJECT_ID",
	"CTXLOOM_ROOT",
}

// Isolate roots HOME at a fresh temp dir and clears every EnvKeys variable for
// the duration of the test, returning the temp home. Because it uses t.Setenv
// (which restores prior values on cleanup and rejects t.Parallel), the calling
// test must not be parallel.
func Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home, for os.UserHomeDir parity
	for _, k := range EnvKeys {
		t.Setenv(k, "")
	}
	return home
}

// ProjectDir isolates the environment (see Isolate) and switches the working
// directory to a fresh temp dir, restoring the original cwd on cleanup. It
// returns the project directory.
func ProjectDir(t *testing.T) string {
	t.Helper()
	Isolate(t)
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("testsupport: getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("testsupport: chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}
