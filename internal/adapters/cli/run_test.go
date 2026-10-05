package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// =============================================================================
// Deterministic --session resume tests
// =============================================================================
// Two modes restored after all flag-based resume + the interactive picker
// were removed: bare --session (full resume, resumeFullContext) and
// --session --compact (compacted resume, resumeCompactEnv). Both are
// IoC-extracted behind an essenceFn/compactFn seam, so they're testable
// without a live session index, backend transcript reader, or shelling out.

// TestValidateResumeFlags covers the friction-up-front gate: --compact only
// modifies HOW --session resumes, so it is meaningless (and rejected) without
// --session — mirroring validatePermissionFlag's "typed now, so strict" style.
func TestValidateResumeFlags(t *testing.T) {
	assert.NoError(t, validateResumeFlags("", false), "neither flag: nothing to validate")
	assert.NoError(t, validateResumeFlags("swift-amber-falcon", false), "bare --session: full resume, valid")
	assert.NoError(t, validateResumeFlags("swift-amber-falcon", true), "--session --compact: compacted resume, valid")

	err := validateResumeFlags("", true)
	assert.Error(t, err, "--compact without --session must be rejected")
	assert.Contains(t, err.Error(), "--compact requires --session")
}

// TestResumeFullContext_FoldsRenderedTranscript covers the happy path: a
// resolvable harp's recorded entries render and join onto the existing
// assembled context via the SAME operations.RenderResumedTranscript/
// textblocks.Join primitives the ACP resume path uses.
func TestResumeFullContext_FoldsRenderedTranscript(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "what does this function do?"},
		{Type: agent.EntryTypeAssistant, Content: "it parses the config file."},
	}
	var requestedHarp string
	entriesFn := func(h string) ([]agent.SessionEntry, error) {
		requestedHarp = h
		return entries, nil
	}

	got := resumeFullContext("existing project context", "swift-amber-falcon", entriesFn)

	assert.Equal(t, "swift-amber-falcon", requestedHarp, "entriesFn is called with the resumed harp")
	assert.Contains(t, got, "existing project context", "the run's own assembled context is preserved")
	assert.Contains(t, got, "Resumed session swift-amber-falcon", "the rendered transcript's lead-block header is folded in")
	assert.Contains(t, got, "what does this function do?")
	assert.Contains(t, got, "it parses the config file.")
}

// TestResumeFullContext_UnresolvableHarpDegrades covers the fault-tolerant
// path (CLAUDE.md): an unknown/unbound --session harp must warn, not block
// the launch — the existing assembled context is returned unchanged.
func TestResumeFullContext_UnresolvableHarpDegrades(t *testing.T) {
	entriesFn := func(string) ([]agent.SessionEntry, error) {
		return nil, errors.New("unknown session")
	}

	got := resumeFullContext("existing project context", "no-such-harp", entriesFn)

	assert.Equal(t, "existing project context", got, "an unresolvable harp must not alter or block on the existing context")
}

// TestResumeFullContext_EmptyExistingContext covers a bare `ctxloom run
// --session <harp>` with no -p/-f/-t/--agent context of its own: the rendered
// transcript becomes the WHOLE assembled context, not an empty string glued
// onto nothing.
func TestResumeFullContext_EmptyExistingContext(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "hello"},
	}
	got := resumeFullContext("", "swift-amber-falcon", func(string) ([]agent.SessionEntry, error) { return entries, nil })
	assert.Contains(t, got, "hello")
	assert.Contains(t, got, "Resumed session swift-amber-falcon")
}

// TestResumeCompactEnv_SkipsCompactWhenEssenceExistsAndCurrent covers the
// idempotency check: an already-compacted, NOT stale harp must not pay for a
// redundant compact before resuming.
func TestResumeCompactEnv_SkipsCompactWhenEssenceExistsAndCurrent(t *testing.T) {
	compactCalled := false
	env := resumeCompactEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return []byte("essence"), nil },
		func(string) bool { return false },
		func(context.Context, string) error { compactCalled = true; return nil },
	)

	assert.False(t, compactCalled, "an existing, current essence must not be re-compacted")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"], "PARTS must include \"session\" so resumePartsIncludeSession's essence gate opens")
}

// TestResumeCompactEnv_CompactsOnDemandWhenMissing covers the "not yet
// compacted" branch --compact promises: essence missing -> compact via the
// SAME `session compact` compactor path (shellOutCompact in production)
// before returning the env pair. staleFn is never even consulted: there is
// nothing to compare staleness against yet.
func TestResumeCompactEnv_CompactsOnDemandWhenMissing(t *testing.T) {
	var compactedHarp string
	staleFnCalled := false
	env := resumeCompactEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return nil, errors.New("no essence yet") },
		func(string) bool { staleFnCalled = true; return false },
		func(_ context.Context, h string) error { compactedHarp = h; return nil },
	)

	assert.Equal(t, "swift-amber-falcon", compactedHarp, "compact-on-demand runs for the resumed harp")
	assert.False(t, staleFnCalled, "staleness is irrelevant when there is no essence to compare against")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"])
}

// TestResumeCompactEnv_RecompactsWhenEssenceIsStale pins a fix:
// path C used to treat "an essence exists" as "the essence is current",
// resuming a /clear'd session from whatever was compacted BEFORE the clear
// forever, as long as some essence file was ever written. A stale essence
// must trigger the same on-demand compact a missing one does.
func TestResumeCompactEnv_RecompactsWhenEssenceIsStale(t *testing.T) {
	var compactedHarp string
	env := resumeCompactEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return []byte("essence from before the clear"), nil },
		func(string) bool { return true },
		func(_ context.Context, h string) error { compactedHarp = h; return nil },
	)

	assert.Equal(t, "swift-amber-falcon", compactedHarp, "a stale essence must be re-compacted, not silently resumed from")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"])
}

// TestResumeCompactEnv_CompactFailureStillReturnsEnv covers the fault-tolerant
// path: a compact-on-demand failure must not block the resume — the env pair
// is still returned (CLAUDE.md: the SessionStart hook's own essence read then
// simply finds nothing and omits the essence block, rather than the whole
// launch failing over a compact hiccup).
func TestResumeCompactEnv_CompactFailureStillReturnsEnv(t *testing.T) {
	assert.NotPanics(t, func() {
		env := resumeCompactEnv("swift-amber-falcon",
			func(string) ([]byte, error) { return nil, errors.New("no essence yet") },
			func(string) bool { return false },
			func(context.Context, string) error { return errors.New("compact boom") },
		)
		assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"], "the env pair is still returned despite the compact failure")
		assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"])
	})
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

// TestShellOutCompact covers the on-demand `session compact` shell-out
// (--session --compact's resumeCompactEnv path). We replace
// the execCommand seam with a fake that returns /bin/true (or echo on
// platforms where true is non-standard), records the invocation
// arguments, and confirms the subprocess sees the expected harp.
//
// execCommand is exec.CommandContext-shaped so a caller can bound or cancel a
// stalled compact, so the fake accepts and ignores a ctx like production code
// does when the caller passes context.Background() (unbounded, same as the
// old exec.Command seam).
func TestShellOutCompact(t *testing.T) {
	var captured []string
	orig := execCommand
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		// Return a real but harmless Cmd. /bin/true exits 0 on Linux/macOS
		// without producing any output. exec.Command never actually runs
		// during construction — only on .Run(), so this is safe even on
		// systems that don't have /bin/true (the test would fail at .Run
		// with a clear error rather than silently passing).
		return exec.Command("/bin/true")
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutCompact(context.Background(), "swift-amber-falcon")
	require.NoError(t, err, "fake /bin/true should succeed")

	require.Len(t, captured, 4, "expected: exe session compact <harp>")
	assert.Equal(t, "session", captured[1])
	assert.Equal(t, "compact", captured[2])
	assert.Equal(t, "swift-amber-falcon", captured[3])
	// captured[0] is os.Executable() result (the test binary path);
	// we don't pin the exact value because it varies between CI and
	// local runs, but it should be non-empty.
	assert.NotEmpty(t, captured[0])
}

// TestShellOutCompact_PropagatesError covers the failure path: when
// the subprocess exits non-zero, shellOutCompact must surface that as
// an error rather than swallowing it, so the caller can warn the user
// instead of resuming as if the compact had succeeded.
func TestShellOutCompact_PropagatesError(t *testing.T) {
	orig := execCommand
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/false") // always exits 1
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutCompact(context.Background(), "any-harp")
	assert.Error(t, err, "non-zero exit must propagate")
}

// The resolveSelfExecutable decision tree (deleted-suffix stripping, PATH
// fallback) is owned and tested by internal/adapters/selfexec.

// TestAwaitDrain_ASignalDuringTheDrainCutsItShort: the session-exit drain is
// bounded in minutes, so a shutdown signal sent while it waits must end the
// wait instead of being swallowed until the drain settles on its own.
func TestAwaitDrain_ASignalDuringTheDrainCutsItShort(t *testing.T) {
	never := make(chan struct{})
	interrupt := make(chan os.Signal, 1)
	interrupt <- syscall.SIGTERM
	assert.False(t, awaitDrain(never, interrupt), "a signal must end the wait on a drain that has not settled")

	settled := make(chan struct{})
	close(settled)
	assert.True(t, awaitDrain(settled, make(chan os.Signal)), "a settled drain reports settled")
}
