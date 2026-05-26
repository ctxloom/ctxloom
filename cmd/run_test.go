package cmd

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
)

// =============================================================================
// Run Command Tests
// =============================================================================
// Full run-command behavior requires real plugin execution; that lives in
// the integration suite. The unit tests below cover the IoC-extracted
// resume-intent decision tree, which has the most branching logic in
// run.go and previously had zero coverage.

// TestRunCommand_Integration documents that run command requires full system
// integration including config loading and plugin execution.
func TestRunCommand_Integration(t *testing.T) {
	t.Skip("Run command requires full system setup - tested in integration tests")
}

func newRunTestMgr(t *testing.T) *sessions.Manager {
	t.Helper()
	mgr, err := sessions.Open(filepath.Join(t.TempDir(), "index.yaml"))
	require.NoError(t, err)
	return mgr
}

func TestResolveResumeIntentWith_SessionFlag(t *testing.T) {
	t.Run("default_restores_both", func(t *testing.T) {
		dec, err := resolveResumeIntentWith(resumeFlags{Session: "swift-amber-falcon"}, nil, "/p", false)
		require.NoError(t, err)
		assert.Equal(t, sessions.ResumeAction, dec.Action)
		assert.Equal(t, "swift-amber-falcon", dec.FromHarp)
		assert.True(t, dec.RestoreSession)
		assert.True(t, dec.RestoreTasks)
	})

	t.Run("no_tasks_modifier", func(t *testing.T) {
		dec, _ := resolveResumeIntentWith(resumeFlags{Session: "swift-amber-falcon", NoTasks: true}, nil, "/p", false)
		assert.True(t, dec.RestoreSession)
		assert.False(t, dec.RestoreTasks, "--no-tasks must suppress task hydration")
	})
}

func TestResolveResumeIntentWith_TasksFromFlag(t *testing.T) {
	dec, err := resolveResumeIntentWith(resumeFlags{TasksFrom: "quiet-silver-meadow"}, nil, "/p", false)
	require.NoError(t, err)
	assert.Equal(t, sessions.ResumeAction, dec.Action)
	assert.Equal(t, "quiet-silver-meadow", dec.FromHarp)
	assert.False(t, dec.RestoreSession, "--tasks-from must NOT restore the prior session essence")
	assert.True(t, dec.RestoreTasks)
}

func TestResolveResumeIntentWith_NewSessionFlag(t *testing.T) {
	dec, err := resolveResumeIntentWith(resumeFlags{NewSession: true}, nil, "/p", true)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_NoFlagsNoTTY(t *testing.T) {
	mgr := newRunTestMgr(t)
	_, err := mgr.AssignHarp("/p", "claude-code")
	require.NoError(t, err)

	// Non-TTY context with no flags should fall through to NewAction
	// even if entries exist — picker requires interactive stdin.
	dec, err := resolveResumeIntentWith(resumeFlags{}, mgr, "/p", false)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_TTYButEmptyIndex(t *testing.T) {
	mgr := newRunTestMgr(t)
	// Fresh index for a project that's never had a session — picker has
	// nothing to show, so we fall through silently.
	dec, err := resolveResumeIntentWith(resumeFlags{}, mgr, "/p", true)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_NilManager(t *testing.T) {
	// If sessions.Open failed (rare; bad HOME), the run path passes a
	// nil manager. We must not panic — silently fall through to new.
	dec, err := resolveResumeIntentWith(resumeFlags{}, nil, "/p", true)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_FlagPrecedence(t *testing.T) {
	// When multiple flags collide, --session wins (first in the switch).
	// Documenting the actual precedence so a future reorder is intentional.
	dec, _ := resolveResumeIntentWith(resumeFlags{
		Session:    "winner",
		TasksFrom:  "loser",
		NewSession: true,
	}, nil, "/p", true)
	assert.Equal(t, "winner", dec.FromHarp, "--session beats --tasks-from + --new-session")
}

// TestShellOutDistill covers the picker's `d<N>` callback. We replace
// the execCommand seam with a fake that returns /bin/true (or echo on
// platforms where true is non-standard), records the invocation
// arguments, and confirms the subprocess sees the expected harp.
func TestShellOutDistill(t *testing.T) {
	var captured []string
	orig := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		// Return a real but harmless Cmd. /bin/true exits 0 on Linux/macOS
		// without producing any output. exec.Command never actually runs
		// during construction — only on .Run(), so this is safe even on
		// systems that don't have /bin/true (the test would fail at .Run
		// with a clear error rather than silently passing).
		return exec.Command("/bin/true")
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutDistill("swift-amber-falcon")
	require.NoError(t, err, "fake /bin/true should succeed")

	require.Len(t, captured, 4, "expected: exe session distill <harp>")
	assert.Equal(t, "session", captured[1])
	assert.Equal(t, "distill", captured[2])
	assert.Equal(t, "swift-amber-falcon", captured[3])
	// captured[0] is os.Executable() result (the test binary path);
	// we don't pin the exact value because it varies between CI and
	// local runs, but it should be non-empty.
	assert.NotEmpty(t, captured[0])
}

// TestShellOutDistill_PropagatesError covers the failure path: when
// the subprocess exits non-zero, shellOutDistill must surface that as
// an error rather than swallowing it. The picker's d<N> handler shows
// the error to the user.
func TestShellOutDistill_PropagatesError(t *testing.T) {
	orig := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/false") // always exits 1
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutDistill("any-harp")
	assert.Error(t, err, "non-zero exit must propagate")
}

// TestResumePartsCSV covers the small helper that encodes the
// CTXLOOM_RESUMED_PARTS env var from a Decision.
func TestResumePartsCSV(t *testing.T) {
	cases := []struct {
		name           string
		session, tasks bool
		want           string
	}{
		{"both", true, true, "session,tasks"},
		{"session_only", true, false, "session"},
		{"tasks_only", false, true, "tasks"},
		{"neither", false, false, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resumePartsCSV(sessions.Decision{
				RestoreSession: tc.session,
				RestoreTasks:   tc.tasks,
			})
			assert.Equal(t, tc.want, got)
		})
	}
}
