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
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
// This IS taskstest.EnvKeys (not a copy) — see that package's doc:
// two separate lists here and there is exactly how one drifted to cover only
// 3 of ~18 variables with nothing to catch it. One list, referenced from
// both names, cannot drift.
var EnvKeys = taskstest.EnvKeys

// Isolate roots HOME at a fresh temp dir and clears every EnvKeys variable for
// the duration of the test, returning the temp home. Because it uses t.Setenv
// (which restores prior values on cleanup and rejects t.Parallel), the calling
// test must not be parallel.
//
// The body lives in taskstest for the same reason ChangeDir's does: the shared
// tree cannot import testsupport, so the canonical body sits shared-side —
// one body, no duplicate. Two bodies is how the EnvKeys lists drifted.
func Isolate(t *testing.T) string {
	t.Helper()
	return taskstest.Isolate(t)
}

// UnderTestTempRoot reports whether path is under a root a test may write to.
// Delegated for the reason given on Isolate.
func UnderTestTempRoot(path string) bool {
	return taskstest.UnderTestTempRoot(path)
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

// PointHomeAt repoints the user's home at dir for the rest of the test:
// HOME and USERPROFILE together, since os.UserHomeDir reads the latter on
// Windows — a test that sets HOME alone shares the runner's real home there.
// Unlike Isolate it clears nothing else, so a test can move the home it
// already isolated (e.g. between a host and a container view).
func PointHomeAt(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// WriteFile creates path's parent directory and then writes data atomically,
// matching the MkdirAll-then-safefs sequence a production writer uses (e.g.
// internal/adapters/operations/signer.go#appendAllowedSignersLine) — so a fixture
// calling this never disagrees with production about what "write a file"
// means. It fails the test immediately on error.
//
// Delegated to taskstest for the same reason ChangeDir is: the shared tree
// cannot import testsupport, so the canonical body lives there. One body, no
// duplicate.
func WriteFile(t testing.TB, fs afero.Fs, path string, data []byte, perm os.FileMode) {
	t.Helper()
	taskstest.WriteFile(t, fs, path, data, perm)
}

// WriteFileString is WriteFile for a string payload. Delegated for the reason
// given on WriteFile.
func WriteFileString(t testing.TB, fs afero.Fs, path, content string, perm os.FileMode) {
	t.Helper()
	taskstest.WriteFileString(t, fs, path, content, perm)
}

// SeedTree writes every entry in files (a path relative to root, mapped to
// its content) through WriteFile, creating whatever nested directories each
// entry needs — the shape roughly forty fixtures across the repo
// re-implement inline as a per-file afero.WriteFile loop. Delegated for the
// reason given on WriteFile.
func SeedTree(t testing.TB, fs afero.Fs, root string, files map[string]string) {
	t.Helper()
	taskstest.SeedTree(t, fs, root, files)
}

// WriteDirProfiles writes one .ctxloom/profiles/<name>.yaml per entry under
// appDir, marshalling each value as YAML.
//
// Values are typically a config.Profile: every field it can carry is spelled
// identically in a directory profile, so marshalling one produces a valid
// profile file. The parameter is `any` rather than that type because this
// package must not import config — config's own comments record that the
// dependency runs the other way, and closing the loop would cycle.
//
// It writes through the caller's afero.Fs, so a memfs test stays on memfs:
// config.ProfileLoaderOptions wires profiles.WithFS from the same fs, which is
// what makes the loader read what was written here.
func WriteDirProfiles(t *testing.T, fs afero.Fs, appDir string, profiles map[string]any) {
	t.Helper()
	dir := filepath.Join(appDir, "profiles")
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	for name, p := range profiles {
		body, err := yaml.Marshal(p)
		require.NoError(t, err, "marshal profile %q", name)
		// A profile name may carry a path ("personal/typescript-dev"), which is
		// a nested file rather than a literal slash in the filename.
		out := filepath.Join(dir, filepath.FromSlash(name)+".yaml")
		require.NoError(t, fs.MkdirAll(filepath.Dir(out), 0o755))
		require.NoError(t, safefs.WriteFile(fs, out, body, 0o644))
	}
}

// RunLoadout wraps a bundle document as the RUN section of a companion
// loadout document — the shape a companion's `loadout --format json`
// envelope carries (bundles.ParseLoadout's input) — for a fixture that only
// needs session-time content.
func RunLoadout(bundleYAML string) []byte {
	var b strings.Builder
	b.WriteString("run:\n")
	for _, line := range strings.Split(strings.TrimRight(bundleYAML, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return []byte(b.String())
}
