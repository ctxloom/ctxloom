package workdir

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestResolveBoundary_EachDistinctInvalidRootWarns pins the diagnostic through
// the chain taskloom itself calls, not just at the step that emits it.
//
// The suppression behind this used to be a package-level sync.Once in THIS
// package, keyed on nothing and consumed by the first invalid CTXLOOM_ROOT the
// process ever saw — so every later offending value, however different, was
// silently ignored, and in a test binary the first test to touch an invalid
// root disarmed the warning for all the rest. The latch is now per-message
// (clidiag.WarnOnce), which is the property this asserts: a DIFFERENT bad
// value is a different fault and must still be reported.
//
// The values are derived from t.TempDir() so their formatted lines are unique
// to this run: clidiag's dedup map is process-global, and a hard-coded path
// could be pre-seeded by another test, leaving this pin green for a reason
// unrelated to the defect.
func TestResolveBoundary_EachDistinctInvalidRootWarns(t *testing.T) {
	taskstest.Isolate(t)
	taskstest.ChangeDir(t, t.TempDir())

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	base := t.TempDir()
	first := filepath.Join(base, "workdir-no-such-root-a")
	second := filepath.Join(base, "workdir-no-such-root-b")

	// The fixture is only hostile if both values really are invalid roots; a
	// path that happened to exist would warn zero times and prove nothing.
	for _, bad := range []string{first, second} {
		_, err := os.Stat(bad)
		require.True(t, os.IsNotExist(err), "fixture is not hostile: %s exists", bad)
	}

	t.Setenv(projectroot.EnvVar, first)
	_, _, err := ResolveBoundary()
	require.NoError(t, err, "a bad override must never block a task operation")
	require.Contains(t, sink.String(), first,
		"fixture is not hostile: the first invalid root produced no warning at all")

	t.Setenv(projectroot.EnvVar, second)
	_, _, err = ResolveBoundary()
	require.NoError(t, err)
	assert.Contains(t, sink.String(), second,
		"a second, DIFFERENT invalid CTXLOOM_ROOT must still be reported through "+
			"ResolveBoundary — the suppression collapses repeats of one message, "+
			"it does not mute the variable")
}

// TestResolveBoundary_RepeatedInvalidRootWarnsOnce pins the half of the
// suppression that must survive: one offending value, resolved as many times
// as a long-lived process resolves it, stays a single line.
func TestResolveBoundary_RepeatedInvalidRootWarnsOnce(t *testing.T) {
	taskstest.Isolate(t)
	taskstest.ChangeDir(t, t.TempDir())

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	bad := filepath.Join(t.TempDir(), "workdir-no-such-root-repeat")
	t.Setenv(projectroot.EnvVar, bad)
	for range 5 {
		_, _, err := ResolveBoundary()
		require.NoError(t, err)
	}
	assert.Equal(t, 1, strings.Count(sink.String(), bad),
		"one offending value warns once however often the boundary is resolved")
}
