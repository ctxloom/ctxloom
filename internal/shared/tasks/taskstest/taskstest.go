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

	"github.com/ctxloom/ctxloom/internal/shared/confload"
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
	ResetProcessOverrides(t)
	return home
}

// ResetProcessOverrides clears confload's process-wide env/CLI override
// capture (see confload.ResetProcessOverrides's doc) for the duration of the
// test: that state outlives any single t.Setenv-scoped var and is shared
// across every test in the binary, so a PRIOR test's (or production code's)
// installed Overrides could otherwise leak into a test that never itself
// touches overrides. This is the CANONICAL body both Isolate here and
// internal/testsupport.Isolate call — testsupport already imports this
// package for ChangeDir, for the identical "one body, no duplicate" reason
// (see ChangeDir's doc): the shared tree must stay self-contained (cannot
// import testsupport) while testsupport may import shared.
func ResetProcessOverrides(t *testing.T) {
	t.Helper()
	confload.ResetProcessOverrides()
	t.Cleanup(confload.ResetProcessOverrides)
}

// ProjectDir isolates the environment (see Isolate) and switches the working
// directory to a fresh temp dir, restoring the original cwd on cleanup. It
// returns the project directory.
func ProjectDir(t *testing.T) string {
	t.Helper()
	Isolate(t)
	dir := t.TempDir()
	ChangeDir(t, dir)
	return dir
}

// ChangeDir switches the working directory to dir for the duration of the
// test, restoring the original on cleanup. It is ProjectDir's os.Chdir
// wrapper, exposed directly for callers that need to chdir into a directory
// they built themselves rather than the fresh temp dir ProjectDir would mint.
// It does not isolate the environment — call Isolate (or ProjectDir, for a
// fresh dir) alongside it when a test needs that too. This is the CANONICAL
// body: internal/testsupport.ChangeDir delegates here, because the shared
// tree must stay self-contained (it cannot import testsupport) while
// testsupport may import shared — one body, no duplicate (reprise).
func ChangeDir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("taskstest: getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("taskstest: chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}
