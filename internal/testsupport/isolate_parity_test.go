package testsupport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// isolationState is everything an isolation helper is responsible for: where
// HOME points and which of the tracked variables still carry a value.
type isolationState struct {
	home        string
	userProfile string
	nonEmpty    []string
}

func observeIsolation(t *testing.T, isolate func(*testing.T) string) isolationState {
	t.Helper()
	// Seed a value into every tracked variable so "cleared" is distinguishable
	// from "was never set".
	for _, k := range taskstest.EnvKeys {
		t.Setenv(k, "ambient-"+k)
	}
	isolate(t)
	var nonEmpty []string
	for _, k := range taskstest.EnvKeys {
		if os.Getenv(k) != "" {
			nonEmpty = append(nonEmpty, k)
		}
	}
	return isolationState{home: os.Getenv("HOME"), userProfile: os.Getenv("USERPROFILE"), nonEmpty: nonEmpty}
}

// TestIsolateParityWithTaskstest pins this package's Isolate to taskstest's.
// Two bodies is how the two EnvKeys lists drifted to cover 3 of ~18 variables
// with nothing to catch it; a test that only exercises one of them cannot see
// the next divergence either.
func TestIsolateParityWithTaskstest(t *testing.T) {
	var mine, theirs isolationState
	t.Run("testsupport", func(t *testing.T) { mine = observeIsolation(t, Isolate) })
	t.Run("taskstest", func(t *testing.T) { theirs = observeIsolation(t, taskstest.Isolate) })

	assert.Equal(t, theirs.nonEmpty, mine.nonEmpty, "the two Isolate bodies clear different variables")
	for _, got := range []isolationState{mine, theirs} {
		assert.NotEmpty(t, got.home)
		assert.Equal(t, got.home, got.userProfile, "USERPROFILE must track HOME for os.UserHomeDir parity")
	}
}

// TestProjectDirParityWithTaskstest pins the same for ProjectDir: an isolated
// environment plus a fresh working directory that is restored on cleanup.
func TestProjectDirParityWithTaskstest(t *testing.T) {
	before, err := os.Getwd()
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		fn   func(*testing.T) string
	}{
		{"testsupport", ProjectDir},
		{"taskstest", taskstest.ProjectDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.fn(t)
			require.NotEmpty(t, dir)
			cwd, err := os.Getwd()
			require.NoError(t, err)
			assert.Equal(t, resolved(t, dir), resolved(t, cwd), "%s: ProjectDir must chdir into the dir it returns", tc.name)
			assert.NotEmpty(t, os.Getenv("HOME"))
		})
		after, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, before, after, "%s: ProjectDir must restore the original cwd on cleanup", tc.name)
	}
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return real
}
