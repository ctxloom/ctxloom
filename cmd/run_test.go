package cmd

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestBuildPickerEntries_DedupMergeSort covers the merge of indexed harp
// sessions with raw backend transcripts: already-indexed transcripts (by path
// or session id) are dropped, survivors become raw rows (empty HarpName), and
// the combined list is most-recent-first.
func TestBuildPickerEntries_DedupMergeSort(t *testing.T) {
	base := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	indexed := []sessions.Entry{
		{HarpName: "swift-amber-falcon", SessionID: "sid-A", TranscriptPath: "/t/sid-A.jsonl", StartedAt: base},
		{HarpName: "quiet-silver-meadow", TranscriptPath: "/t/sid-B.jsonl", StartedAt: base.Add(-2 * time.Hour)},
	}
	raw := []agent.SessionMeta{
		{ID: "sid-A", Path: "/t/sid-A.jsonl", StartTime: base},                     // dup by path+id → dropped
		{ID: "sid-B", Path: "/t/sid-B.jsonl", StartTime: base},                     // dup by path → dropped
		{ID: "sid-C", Path: "/t/sid-C.jsonl", StartTime: base.Add(-1 * time.Hour)}, // new → kept as raw
		{ID: "", Path: "", StartTime: base},                                        // empty → skipped
	}

	got := buildPickerEntries(indexed, raw, "claude-code")
	require.Len(t, got, 3, "2 indexed + 1 surviving raw")
	// Most-recent-first: sid-A (base), sid-C (base-1h), meadow (base-2h).
	assert.Equal(t, "swift-amber-falcon", got[0].HarpName)
	assert.Equal(t, "", got[1].HarpName, "raw row carries empty harp (the picker sentinel)")
	assert.Equal(t, "sid-C", got[1].SessionID)
	assert.Equal(t, "/t/sid-C.jsonl", got[1].TranscriptPath)
	assert.Equal(t, "claude-code", got[1].Backend)
	assert.Equal(t, "quiet-silver-meadow", got[2].HarpName)
}

// TestBuildPickerEntries_DedupBySessionID confirms a raw transcript is deduped
// against an indexed entry by session id even when the index recorded no
// transcript path (the bind hook hadn't supplied one).
func TestBuildPickerEntries_DedupBySessionID(t *testing.T) {
	indexed := []sessions.Entry{{HarpName: "h", SessionID: "sid-X"}}
	raw := []agent.SessionMeta{{ID: "sid-X", Path: "/t/sid-X.jsonl"}}
	got := buildPickerEntries(indexed, raw, "claude-code")
	require.Len(t, got, 1, "raw matched by session id is deduped despite empty index transcript path")
	assert.Equal(t, "h", got[0].HarpName)
}

func TestBuildPickerEntries_NoRaw(t *testing.T) {
	indexed := []sessions.Entry{{HarpName: "h"}}
	got := buildPickerEntries(indexed, nil, "claude-code")
	require.Len(t, got, 1)
	assert.Equal(t, "h", got[0].HarpName)
}

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

func TestResolveResumeIntentWith_SessionFlag(t *testing.T) {
	t.Run("default_restores_both", func(t *testing.T) {
		dec, err := resolveResumeIntentWith(resumeFlags{Session: "swift-amber-falcon"}, nil, "/p", false, "", nil, nil)
		require.NoError(t, err)
		assert.Equal(t, sessions.ResumeAction, dec.Action)
		assert.Equal(t, "swift-amber-falcon", dec.FromHarp)
		assert.True(t, dec.RestoreSession)
		assert.True(t, dec.RestoreTasks)
	})

	t.Run("no_tasks_modifier", func(t *testing.T) {
		dec, _ := resolveResumeIntentWith(resumeFlags{Session: "swift-amber-falcon", NoTasks: true}, nil, "/p", false, "", nil, nil)
		assert.True(t, dec.RestoreSession)
		assert.False(t, dec.RestoreTasks, "--no-tasks must suppress task hydration")
	})
}

func TestResolveResumeIntentWith_TasksFromFlag(t *testing.T) {
	dec, err := resolveResumeIntentWith(resumeFlags{TasksFrom: "quiet-silver-meadow"}, nil, "/p", false, "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, sessions.ResumeAction, dec.Action)
	assert.Equal(t, "quiet-silver-meadow", dec.FromHarp)
	assert.False(t, dec.RestoreSession, "--tasks-from must NOT restore the prior session essence")
	assert.True(t, dec.RestoreTasks)
}

func TestResolveResumeIntentWith_NewSessionFlag(t *testing.T) {
	dec, err := resolveResumeIntentWith(resumeFlags{NewSession: true}, nil, "/p", true, "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_NoFlagsNoTTY(t *testing.T) {
	indexed := []sessions.Entry{{HarpName: "swift-amber-falcon", ProjectDir: "/p"}}
	// Non-TTY context with no flags should fall through to NewAction
	// even if entries exist — picker requires interactive stdin.
	dec, err := resolveResumeIntentWith(resumeFlags{}, indexed, "/p", false, "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, sessions.NewAction, dec.Action)
}

func TestResolveResumeIntentWith_TTYButEmptyIndex(t *testing.T) {
	// No indexed sessions and no raw transcripts (nil hist) — the picker has
	// nothing to show, so we fall through silently. Nil entries also models the
	// case where the caller's best-effort index read failed (open error / bad
	// HOME): the seam must not panic, just start fresh.
	dec, err := resolveResumeIntentWith(resumeFlags{}, nil, "/p", true, "", nil, nil)
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
	}, nil, "/p", true, "", nil, nil)
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

// The resolveSelfExecutable decision tree (deleted-suffix stripping, PATH
// fallback) is owned and tested by internal/selfexec.

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
