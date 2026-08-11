package taskstest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsolate_ClearsVarsBeyondTheOriginalThree pins the fix: this package's
// own EnvKeys had drifted to cover only 3 of the ~18 variables
// internal/testsupport.EnvKeys covered ("CTXLOOM_SESSION_HARP",
// "CTXLOOM_PROJECT_ID", "CTXLOOM_ROOT"), with no guard test to catch the
// drift — so any of taskstest.Isolate's 50+ direct callers believed itself
// isolated and was not. These three were NOT in the old list; Isolate must
// clear them now that EnvKeys is the canonical, complete list (shared with
// testsupport).
func TestIsolate_ClearsVarsBeyondTheOriginalThree(t *testing.T) {
	previouslyMissing := []string{
		// The exact incident EnvKeys' own comment records: an ambient
		// CTXLOOM_MCP_SOCKET silently flips `ctxloom mcp serve` into
		// forward-mode against the REAL coordinator inside a test.
		"CTXLOOM_MCP_SOCKET",
		"CTXLOOM_RESUMED_FROM",
		"CTXLOOM_DEGRADED",
	}
	for _, k := range previouslyMissing {
		t.Setenv(k, "ambient-leaked-value")
	}

	Isolate(t)

	for _, k := range previouslyMissing {
		assert.Empty(t, os.Getenv(k), "Isolate must clear %s, not just the original three", k)
	}
}

// TestEnvKeys_CoversTheFullProductionSet is a lightweight regression pin
// against EnvKeys narrowing back down: every variable known (at the time of
// the fix) to matter to isolation must still be present.
func TestEnvKeys_CoversTheFullProductionSet(t *testing.T) {
	known := make(map[string]bool, len(EnvKeys))
	for _, k := range EnvKeys {
		known[k] = true
	}
	for _, want := range []string{
		"CTXLOOM_SESSION_HARP", "CTXLOOM_PROJECT_ID", "CTXLOOM_ROOT",
		"CTXLOOM_RESUMED_FROM", "CTXLOOM_RESUMED_PARTS", "CTXLOOM_DEBUG_HTTP",
		"CTXLOOM_DEGRADED", "CTXLOOM_VERBOSE", "CTXLOOM_NO_COMPANIONS",
		"CTXLOOM_MCP_SOCKET", "CTXLOOM_COORD_URL", "CTXLOOM_COORD_CRED",
		"CTXLOOM_RUN_ID", "CTXLOOM_CELL_WORKDIR", "CTXLOOM_RUN_DEPTH",
		"CTXLOOM_ISOLATION_PROBE_TRACE_DIR", "CTXLOOM_LAUNCH_MAX_ATTEMPTS",
		"CTXLOOM_LAUNCH_BACKOFF_BASE", "CTXLOOM_LAUNCH_BACKOFF_MAX",
		"CTXLOOM_REQUIRE_DOCKER", "CTXLOOM_REQUIRE_RUNTIMES", "CTXLOOM_CELL_BINARY",
	} {
		assert.True(t, known[want], "EnvKeys is missing %s", want)
	}
}

// TestChangeDir_RestoresTheOriginalCwd pins the half of this package that no
// dependent test can notice breaking: a ChangeDir that failed to restore the
// working directory would leave every LATER test in the binary running in a
// temp dir that t.TempDir has already removed, and the resulting failures
// would point anywhere but here.
func TestChangeDir_RestoresTheOriginalCwd(t *testing.T) {
	before, err := os.Getwd()
	require.NoError(t, err)

	target := t.TempDir()
	t.Run("inside", func(t *testing.T) {
		ChangeDir(t, target)
		cwd, err := os.Getwd()
		require.NoError(t, err)
		want, err := filepath.EvalSymlinks(target)
		require.NoError(t, err)
		got, err := filepath.EvalSymlinks(cwd)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	after, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// TestProjectDir_IsolatesAndChangesDir pins the composition: an isolated
// environment AND a fresh working directory, both undone on cleanup.
func TestProjectDir_IsolatesAndChangesDir(t *testing.T) {
	before, err := os.Getwd()
	require.NoError(t, err)

	t.Run("inside", func(t *testing.T) {
		t.Setenv("CTXLOOM_SESSION_HARP", "ambient-harp")
		dir := ProjectDir(t)
		cwd, err := os.Getwd()
		require.NoError(t, err)
		want, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		got, err := filepath.EvalSymlinks(cwd)
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Empty(t, os.Getenv("CTXLOOM_SESSION_HARP"), "ProjectDir must isolate as well as chdir")
	})

	after, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// recordingReporter captures what restoreDir reports instead of failing the
// test that is asserting on it.
type recordingReporter struct{ msgs []string }

func (r *recordingReporter) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

// TestRestoreDir_ReportsAFailedRestore pins the branch that used to be
// `_ = os.Chdir(orig)`. A discarded restore error is worse than a local
// failure: the cwd is process-global, so every subsequent test in the binary
// runs from a directory it never chose, and the resulting failures surface
// arbitrarily far from the cause. Nothing anywhere reported the one fact that
// explains them.
func TestRestoreDir_ReportsAFailedRestore(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "removed")
	require.NoError(t, os.MkdirAll(gone, 0o755))
	require.NoError(t, os.RemoveAll(gone))

	var rep recordingReporter
	restoreDir(&rep, gone, "/somewhere-the-process-is-left")

	require.Len(t, rep.msgs, 1, "a failed restore must be reported, not discarded")
	assert.Contains(t, rep.msgs[0], gone, "the message must name the directory it could not return to")
	assert.Contains(t, rep.msgs[0], "/somewhere-the-process-is-left",
		"and where the process is actually left standing")
}

// TestRestoreDir_SilentOnSuccess is the negative case: the ordinary restore
// must stay quiet, or every test using ChangeDir would fail.
func TestRestoreDir_SilentOnSuccess(t *testing.T) {
	// Restore to where the process already is: a successful no-op chdir, so
	// this test cannot disturb the working directory every other test in the
	// binary shares (and does not need the os.Chdir the linter forbids in
	// tests for exactly that reason).
	here, err := os.Getwd()
	require.NoError(t, err)

	var rep recordingReporter
	restoreDir(&rep, here, "unused")
	assert.Empty(t, rep.msgs)
}

// TestPackageDoc_GeneralPurposeClaimHolds pins the invariant the package doc
// asserts. Both the package doc and EnvKeys' doc once scoped this package to
// "the task store"; that narrow framing is what made a narrow EnvKeys look
// correct while 50-odd callers relying on it for general isolation were not
// isolated at all. The docs now say GENERAL-purpose and cite the
// call-site count as the evidence.
//
// A doc claim nobody measures drifts back. This measures it: if the count ever
// falls below the number the prose states, either the prose is stale or the
// package really has narrowed, and both deserve a look.
//
// The scan root comes from runtime.Caller, NOT the working directory. A
// source-scanning gate rooted at "." walks whatever temp directory the binary
// happens to be in, finds nothing, and passes — a gate that evaporates rather
// than fails. This is the same idiom internal/cli's pkgSourceDir documents.
func TestPackageDoc_GeneralPurposeClaimHolds(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller: cannot locate this source file")
	// internal/shared/tasks/taskstest -> repo root
	repo := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))))
	require.FileExists(t, filepath.Join(repo, "go.mod"),
		"the scan root must be the repo, or this test measures nothing")

	// Call SITES, not files: a single test file routinely isolates in a dozen
	// test functions, and the doc's claim is about how widely the helper is
	// relied on, not how it is distributed across files.
	sites, files := 0, 0
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasPrefix(path, filepath.Join(repo, "internal", "shared", "tasks", "taskstest")) {
			return nil // the package itself is not a caller
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		before := sites
		for _, fn := range []string{"taskstest.Isolate(", "taskstest.ProjectDir(", "taskstest.ChangeDir("} {
			sites += strings.Count(string(b), fn)
		}
		if sites > before {
			files++
		}
		return nil
	})
	require.NoError(t, err)

	assert.GreaterOrEqual(t, sites, 50,
		"the package doc claims well over 50 call sites use this as general-purpose test "+
			"isolation; found %d across %d files. Either the doc is stale or the package narrowed",
		sites, files)
}
