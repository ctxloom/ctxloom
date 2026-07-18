package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// warnBypassOnLostContainer must warn ONLY when a container-requested run
// actually lost its boundary: any container-BACKED prepared policy (container
// or container-worktree) is a satisfied request. The original inline predicate
// compared the prepared name against exactly "container", so a SUCCESSFUL
// container-worktree run warned "running with bypass on the host" — false.
func TestWarnBypassOnLostContainer(t *testing.T) {
	containerAxes := isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeContainer}

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

// TestShellOutDistill covers the exit-path (and, formerly, the picker's)
// `session distill` shell-out. We replace
// the execCommand seam with a fake that returns /bin/true (or echo on
// platforms where true is non-standard), records the invocation
// arguments, and confirms the subprocess sees the expected harp.
//
// execCommand is exec.CommandContext-shaped (FINDING #3: the exit-path
// caller needs to be able to kill a stalled distill), so the fake accepts
// and ignores a ctx like production code does when the caller passes
// context.Background() (unbounded, same as the old exec.Command seam).
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
// an error rather than swallowing it. The picker's d<N> handler shows
// the error to the user.
func TestShellOutDistill_PropagatesError(t *testing.T) {
	orig := execCommand
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/false") // always exits 1
	}
	t.Cleanup(func() { execCommand = orig })

	err := shellOutDistill(context.Background(), "any-harp")
	assert.Error(t, err, "non-zero exit must propagate")
}

// TestShouldDistillOnExit covers the exit-time distill decision: a bound
// harp on an INTERACTIVE run should distill; an unbound harp, a
// structured-REPL run (whose Chat RPC never runs Setup, so no session_id
// ever binds), or a headless oneshot/--print run must not — FINDING #2:
// distilling on every --print invocation is a blocking LLM call on every
// headless call, and a fresh harp each time means the idempotency guard
// never helps.
func TestShouldDistillOnExit(t *testing.T) {
	assert.True(t, shouldDistillOnExit("swift-amber-falcon", true), "bound harp, interactive run: must distill")
	assert.False(t, shouldDistillOnExit("", true), "no bound harp: nothing to distill")
	assert.False(t, shouldDistillOnExit("swift-amber-falcon", false), "non-interactive (oneshot/--print or structured-REPL) run: must not distill")
	assert.False(t, shouldDistillOnExit("", false), "neither bound nor eligible")
}

// testDistillTimeout is a generous bound used by tests that expect the
// injected distillFn to return immediately — large enough that it never
// fires in practice, so these tests exercise the happy/error paths rather
// than the FINDING #3 timeout path (covered separately below).
const testDistillTimeout = 5 * time.Second

// TestDistillSessionOnExit_DistillsExactlyOnce covers the happy path: a
// clean exit with a bound harp on an INTERACTIVE run and no pre-existing
// essence distills exactly once for that harp.
func TestDistillSessionOnExit_DistillsExactlyOnce(t *testing.T) {
	var essenceCalls, distillCalls []string
	essenceFn := func(harp string) ([]byte, error) {
		essenceCalls = append(essenceCalls, harp)
		return nil, errors.New("no essence yet")
	}
	distillFn := func(_ context.Context, harp string) error {
		distillCalls = append(distillCalls, harp)
		return nil
	}
	var out bytes.Buffer

	distillSessionOnExit("swift-amber-falcon", true, essenceFn, distillFn, testDistillTimeout, &out)

	assert.Equal(t, []string{"swift-amber-falcon"}, essenceCalls, "idempotency check runs once")
	assert.Equal(t, []string{"swift-amber-falcon"}, distillCalls, "distill runs exactly once for the active harp")
	assert.Contains(t, out.String(), "distilling session swift-amber-falcon")
	assert.Contains(t, out.String(), "distilled session swift-amber-falcon")
}

// TestDistillSessionOnExit_NoOpCases covers the guarded no-op cases: an
// empty harp, a non-interactive run (structured-REPL or oneshot/--print),
// and a harp whose essence already exists must never shell out to distill.
// FINDING #2: a headless `ctxloom run -p X --print` must not pay for a
// blocking LLM call on every invocation.
func TestDistillSessionOnExit_NoOpCases(t *testing.T) {
	t.Run("empty harp", func(t *testing.T) {
		distillCalled := false
		distillSessionOnExit("", true,
			func(string) ([]byte, error) { return nil, errors.New("no essence") },
			func(context.Context, string) error { distillCalled = true; return nil },
			testDistillTimeout,
			io.Discard)
		assert.False(t, distillCalled, "no bound harp: nothing to distill")
	})

	t.Run("non-interactive run (structured REPL or oneshot/--print)", func(t *testing.T) {
		distillCalled := false
		distillSessionOnExit("swift-amber-falcon", false,
			func(string) ([]byte, error) { return nil, errors.New("no essence") },
			func(context.Context, string) error { distillCalled = true; return nil },
			testDistillTimeout,
			io.Discard)
		assert.False(t, distillCalled, "structured runs never bind a session_id, and headless oneshot/--print must not pay for a blocking LLM call on every invocation — skip")
	})

	t.Run("already distilled", func(t *testing.T) {
		distillCalled := false
		distillSessionOnExit("swift-amber-falcon", true,
			func(string) ([]byte, error) { return []byte("essence"), nil }, // essence exists
			func(context.Context, string) error { distillCalled = true; return nil },
			testDistillTimeout,
			io.Discard)
		assert.False(t, distillCalled, "idempotent: an existing essence must not be redistilled")
	})
}

// TestDistillSessionOnExit_DistillFailureWarnsNotPanics covers the failure
// path: a distill error must be swallowed (warned) rather than propagated —
// this runs from a defer in the exit path, where there is no caller left to
// hand an error to.
func TestDistillSessionOnExit_DistillFailureWarnsNotPanics(t *testing.T) {
	var out bytes.Buffer
	assert.NotPanics(t, func() {
		distillSessionOnExit("swift-amber-falcon", true,
			func(string) ([]byte, error) { return nil, errors.New("no essence") },
			func(context.Context, string) error { return errors.New("boom") },
			testDistillTimeout,
			&out)
	})
	assert.Contains(t, out.String(), "distilling session swift-amber-falcon", "the visible pre-distill message still prints")
	assert.NotContains(t, out.String(), "distilled session swift-amber-falcon", "no false success line on failure")
}

// TestDistillSessionOnExit_BoundedByTimeout is the FINDING #3 regression
// test: a stalled distillFn (simulating a wedged LLM/network call) must not
// block distillSessionOnExit forever. The fake ignores its ctx entirely
// (blocks on an unbuffered channel that never receives) so boundedness comes
// from distillSessionOnExit's own timeout plumbing, not cooperative
// cancellation by the callee. A short injected timeout keeps the test fast;
// production wires a real 120s (exitDistillTimeout).
func TestDistillSessionOnExit_BoundedByTimeout(t *testing.T) {
	var out bytes.Buffer
	hang := make(chan struct{}) // never closed/sent to — simulates a wedged call
	distillFn := func(context.Context, string) error {
		<-hang
		return nil
	}

	const shortTimeout = 30 * time.Millisecond
	start := time.Now()
	distillSessionOnExit("swift-amber-falcon", true,
		func(string) ([]byte, error) { return nil, errors.New("no essence") },
		distillFn,
		shortTimeout,
		&out)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second, "must return once the injected timeout fires, not block on the hung call")
	assert.Contains(t, out.String(), "ctxloom: distillation timed out; it will complete on next startup")
	assert.NotContains(t, out.String(), "distilled session swift-amber-falcon", "no false success line on timeout")
}

// The resolveSelfExecutable decision tree (deleted-suffix stripping, PATH
// fallback) is owned and tested by internal/selfexec.
