// Package testsupport provides shared isolation helpers so tests never read or
// write host or session state — neither the process environment, the user's
// ~/.ctxloom home (resolved via os.UserHomeDir), nor the working directory.
//
// Every test that exercises code reading those should route through Isolate or
// ProjectDir rather than reimplementing the isolation. A test inheriting the
// ambient session's environment is non-deterministic: e.g. CTXLOOM_PROJECT_ID
// selects the live task log, so an un-isolated task test reads the running
// session's tasks instead of its own.
package testsupport

import (
	"os"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// EnvKeys is the canonical set of host/session environment variables ctxloom
// production code reads (plus one deliberate exception, CTXLOOM_REQUIRE_DOCKER
// — testsupport/dockergate's CI-only knob, not production state; see
// taskstest.EnvKeys' own comment for why it is listed here anyway). Isolate
// clears each so a test inherits none of the ambient session's values.
// TestEnvKeysCoversProductionReads enforces that every CTXLOOM_* variable read
// in production appears here.
//
// This IS taskstest.EnvKeys (not a copy) — see that package's doc (U127-F01):
// two separate lists here and there is exactly how one drifted to cover only
// 3 of ~18 variables with nothing to catch it. One list, referenced from
// both names, cannot drift.
var EnvKeys = taskstest.EnvKeys

// Isolate roots HOME at a fresh temp dir and clears every EnvKeys variable for
// the duration of the test, returning the temp home. Because it uses t.Setenv
// (which restores prior values on cleanup and rejects t.Parallel), the calling
// test must not be parallel.
//
// The body lives in taskstest, for the same reason ChangeDir's does: the
// shared tree must stay self-contained (it cannot import testsupport) while
// testsupport may import shared, so the canonical body has to sit shared-side
// — one body, no duplicate. Two bodies is how the two EnvKeys lists drifted
// apart with nothing to catch it.
func Isolate(t *testing.T) string {
	t.Helper()
	return taskstest.Isolate(t)
}

// ProjectDir isolates the environment (see Isolate) and switches the working
// directory to a fresh temp dir, restoring the original cwd on cleanup. It
// returns the project directory. Delegated for the reason given on Isolate.
func ProjectDir(t *testing.T) string {
	t.Helper()
	return taskstest.ProjectDir(t)
}

// ChangeDir switches the working directory to dir for the duration of the
// test, restoring the original on cleanup. It is ProjectDir's os.Chdir
// wrapper, exposed directly for callers that need to chdir into a directory
// they built themselves (e.g. a git-worktree fixture) rather than the fresh
// temp dir ProjectDir would mint. It does not isolate the environment — call
// Isolate (or ProjectDir, for a fresh dir) alongside it when a test needs
// that too. golangci-lint's forbidigo rule forbids os.Chdir directly in test
// files precisely so callers route through here instead.
//
// The body lives in taskstest: the internal/shared tree is self-contained
// (never imports non-shared internal packages, so it can split back out to
// the companion module), which forces the canonical helper shared-side;
// this side delegates rather than duplicating it (reprise).
func ChangeDir(t *testing.T, dir string) {
	t.Helper()
	taskstest.ChangeDir(t, dir)
}

// ScrubbedEnv returns an environment slice for a subprocess spawned by a test:
// the current environment with HOME (and USERPROFILE) rooted at a fresh temp dir
// and every EnvKeys variable removed, so the child does not inherit the host or
// session HOME/env. Use it as exec.Cmd.Env — it is the subprocess analog of
// Isolate, which only governs the in-process environment.
func ScrubbedEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	drop := map[string]bool{"HOME": true, "USERPROFILE": true}
	for _, k := range EnvKeys {
		drop[k] = true
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home}
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		env = append(env, kv)
	}
	return env
}
