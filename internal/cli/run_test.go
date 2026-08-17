package cli

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// =============================================================================
// Deterministic --session resume tests
// =============================================================================
// Two modes restored after all flag-based resume + the interactive picker
// were removed: bare --session (full resume, resumeFullContext) and
// --session --distill (distilled resume, resumeDistillEnv). Both are
// IoC-extracted behind an essenceFn/distillFn seam, so they're testable
// without a live session index, backend transcript reader, or shelling out.

// TestValidateResumeFlags covers the friction-up-front gate: --distill only
// modifies HOW --session resumes, so it is meaningless (and rejected) without
// --session — mirroring validatePermissionFlag's "typed now, so strict" style.
func TestValidateResumeFlags(t *testing.T) {
	assert.NoError(t, validateResumeFlags("", false), "neither flag: nothing to validate")
	assert.NoError(t, validateResumeFlags("swift-amber-falcon", false), "bare --session: full resume, valid")
	assert.NoError(t, validateResumeFlags("swift-amber-falcon", true), "--session --distill: distilled resume, valid")

	err := validateResumeFlags("", true)
	assert.Error(t, err, "--distill without --session must be rejected")
	assert.Contains(t, err.Error(), "--distill requires --session")
}

// TestResumeFullContext_FoldsRenderedTranscript covers the happy path: a
// resolvable harp's recorded entries render and join onto the existing
// assembled context via the SAME operations.RenderResumedTranscript/
// JoinLeadBlocks primitives the ACP resume path uses.
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

// TestResumeDistillEnv_SkipsDistillWhenEssenceExistsAndCurrent covers the
// idempotency check: an already-distilled, NOT stale harp must not pay for a
// redundant distill before resuming.
func TestResumeDistillEnv_SkipsDistillWhenEssenceExistsAndCurrent(t *testing.T) {
	distillCalled := false
	env := resumeDistillEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return []byte("essence"), nil },
		func(string) bool { return false },
		func(context.Context, string) error { distillCalled = true; return nil },
	)

	assert.False(t, distillCalled, "an existing, current essence must not be redistilled")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"], "PARTS must include \"session\" so resumePartsIncludeSession's essence gate opens")
}

// TestResumeDistillEnv_DistillsOnDemandWhenMissing covers the "not yet
// distilled" branch --distill promises: essence missing -> distill via the
// SAME `session distill` compactor path (shellOutDistill in production)
// before returning the env pair. staleFn is never even consulted: there is
// nothing to compare staleness against yet.
func TestResumeDistillEnv_DistillsOnDemandWhenMissing(t *testing.T) {
	var distilledHarp string
	staleFnCalled := false
	env := resumeDistillEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return nil, errors.New("no essence yet") },
		func(string) bool { staleFnCalled = true; return false },
		func(_ context.Context, h string) error { distilledHarp = h; return nil },
	)

	assert.Equal(t, "swift-amber-falcon", distilledHarp, "distill-on-demand runs for the resumed harp")
	assert.False(t, staleFnCalled, "staleness is irrelevant when there is no essence to compare against")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"])
}

// TestResumeDistillEnv_RedistillsWhenEssenceIsStale pins a fix:
// path C used to treat "an essence exists" as "the essence is current",
// resuming a /clear'd session from whatever was distilled BEFORE the clear
// forever, as long as some essence file was ever written. A stale essence
// must trigger the same on-demand distill a missing one does.
func TestResumeDistillEnv_RedistillsWhenEssenceIsStale(t *testing.T) {
	var distilledHarp string
	env := resumeDistillEnv("swift-amber-falcon",
		func(string) ([]byte, error) { return []byte("essence from before the clear"), nil },
		func(string) bool { return true },
		func(_ context.Context, h string) error { distilledHarp = h; return nil },
	)

	assert.Equal(t, "swift-amber-falcon", distilledHarp, "a stale essence must be redistilled, not silently resumed from")
	assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"])
	assert.Equal(t, "session", env["CTXLOOM_RESUMED_PARTS"])
}

// TestResumeDistillEnv_DistillFailureStillReturnsEnv covers the fault-tolerant
// path: a distill-on-demand failure must not block the resume — the env pair
// is still returned (CLAUDE.md: the SessionStart hook's own essence read then
// simply finds nothing and omits the essence block, rather than the whole
// launch failing over a distill hiccup).
func TestResumeDistillEnv_DistillFailureStillReturnsEnv(t *testing.T) {
	assert.NotPanics(t, func() {
		env := resumeDistillEnv("swift-amber-falcon",
			func(string) ([]byte, error) { return nil, errors.New("no essence yet") },
			func(string) bool { return false },
			func(context.Context, string) error { return errors.New("distill boom") },
		)
		assert.Equal(t, "swift-amber-falcon", env["CTXLOOM_RESUMED_FROM"], "the env pair is still returned despite the distill failure")
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

// warnBypassOnLostContainer must warn ONLY when a container-requested run
// actually lost its boundary: any container-BACKED prepared policy (container
// or container-worktree) is a satisfied request. The original inline predicate
// compared the prepared name against exactly "container", so a SUCCESSFUL
// container-worktree run warned "running with bypass on the host" — false.
func TestWarnBypassOnLostContainer(t *testing.T) {
	containerAxes := isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeContainerRootless}

	t.Run("successful container-worktree run does not warn", func(t *testing.T) {
		assert.False(t, warnBypassOnLostContainer(containerAxes, "container-worktree", agent.PermissionBypass, "codex"),
			"container-worktree IS a container boundary — a successful sandboxed run must not warn")
	})

	t.Run("successful plain-container run does not warn", func(t *testing.T) {
		assert.False(t, warnBypassOnLostContainer(containerAxes, "container", agent.PermissionBypass, "codex"))
	})

	t.Run("genuine degrade to the host warns", func(t *testing.T) {
		assert.True(t, warnBypassOnLostContainer(containerAxes, "worktree", agent.PermissionBypass, "codex"),
			"container requested, worktree prepared → the boundary is lost; bypass on the host must warn")
		assert.True(t, warnBypassOnLostContainer(containerAxes, "none", agent.PermissionBypass, "codex"))
	})

	t.Run("no warning without a container request, without bypass, or for the claude-code stopgap", func(t *testing.T) {
		hostAxes := isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeHost}
		assert.False(t, warnBypassOnLostContainer(hostAxes, "worktree", agent.PermissionBypass, "codex"),
			"no container was requested — nothing was lost")
		assert.False(t, warnBypassOnLostContainer(containerAxes, "none", agent.PermissionDefault, "codex"),
			"a prompting posture on the host is the normal degrade, not a silent full-auto")
		assert.False(t, warnBypassOnLostContainer(containerAxes, "none", agent.PermissionBypass, config.BackendClaudeCode),
			"bypass-on-host is the claude-code stopgap's intended posture")
	})
}

// TestShellOutDistill covers the on-demand `session distill` shell-out
// (--session --distill's resumeDistillEnv path). We replace
// the execCommand seam with a fake that returns /bin/true (or echo on
// platforms where true is non-standard), records the invocation
// arguments, and confirms the subprocess sees the expected harp.
//
// execCommand is exec.CommandContext-shaped so a caller can bound or cancel a
// stalled distill, so the fake accepts and ignores a ctx like production code
// does when the caller passes context.Background() (unbounded, same as the
// old exec.Command seam).
func TestShellOutDistill(t *testing.T) {
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

	err := shellOutDistill(context.Background(), "swift-amber-falcon")
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
// an error rather than swallowing it, so the caller can warn the user
// instead of resuming as if the distill had succeeded.
func TestShellOutDistill_PropagatesError(t *testing.T) {
	orig := execCommand
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/false") // always exits 1
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutDistill(context.Background(), "any-harp")
	assert.Error(t, err, "non-zero exit must propagate")
}

// The resolveSelfExecutable decision tree (deleted-suffix stripping, PATH
// fallback) is owned and tested by internal/selfexec.
