package taskstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsolate_ClearsVarsBeyondTheOriginalThree pins U127-F01: this package's
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
// U127-F01's fix) to matter to isolation must still be present.
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
		"CTXLOOM_RUN_ID", "CTXLOOM_CELL_WORKDIR", "CTXLOOM_AGENT_COORDINATOR",
		"CTXLOOM_ISOLATION_PROBE_TRACE_DIR", "CTXLOOM_LAUNCH_MAX_ATTEMPTS",
		"CTXLOOM_LAUNCH_BACKOFF_BASE", "CTXLOOM_LAUNCH_BACKOFF_MAX",
		"CTXLOOM_REQUIRE_DOCKER",
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
