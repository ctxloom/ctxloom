package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestPanicGuard exists only to be run as a subprocess by
// TestMainSandbox_SurvivesCrash_ThenGetsReaped: it panics on demand so that
// test can observe testsupport.SandboxedMain's behavior when m.Run() never
// returns normally. Left un-gated, a panicking test would fail every real
// test run, so it only fires when a subprocess explicitly asks for it.
func TestPanicGuard(t *testing.T) {
	if os.Getenv("CTXLOOM_OPTEST_FORCE_PANIC") != "1" {
		t.Skip("subprocess-only guard test; set CTXLOOM_OPTEST_FORCE_PANIC=1 to invoke")
	}
	panic("ctxloom-optest: intentional panic exercising SandboxedMain's crash path")
}

// pkgDir locates this package's source directory independent of the current
// working directory: TestMain chdirs into a throwaway sandbox for the whole
// run, so os.Getwd() cannot be used to find where `go test` must be invoked.
func pkgDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must resolve this file's path")
	return filepath.Dir(thisFile)
}

// runOperationsSubprocess runs `go test -run <pattern>` on this package in a
// fresh process with TMPDIR pinned to tmpDir, so the child's sandbox (and
// this test's assertions about it) are confined to a throwaway root instead
// of the developer's real /tmp.
//
// The parent's own sandbox marker is STRIPPED from the child's environment:
// SandboxedMain treats an inherited testsupport.SandboxRootEnv as "already
// inside a sandbox" and adopts it instead of minting its own — the right
// call for a re-exec'd child, and exactly wrong here, where the child's own
// sandbox is the thing under observation. HOME is left as the parent's
// sandbox home on purpose: SandboxedMain pins the go toolchain's cache
// directories explicitly, so the child's `go test` build phase does not need
// the real HOME to find them.
func runOperationsSubprocess(t *testing.T, tmpDir, pattern string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "test", "-run", pattern, "-count=1", ".")
	cmd.Dir = pkgDir(t)
	env := []string{"TMPDIR=" + tmpDir}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, testsupport.SandboxRootEnv+"=") {
			env = append(env, kv)
		}
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

// sandboxLeftovers lists the pid-directory names currently under the shared
// sandbox root inside tmpDir, or nil if the root doesn't exist yet.
func sandboxLeftovers(t *testing.T, tmpDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(tmpDir, testsupport.SandboxRootName))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestMainSandbox_SurvivesCrash_ThenGetsReaped proves both halves of
// SandboxedMain's crash-path story against a REAL sandboxed process: a test
// that panics still crashes its process without running any deferred cleanup
// (so its sandbox dir is left behind — this is unavoidable and is not itself
// the bug), and the NEXT process to start reaps that dead process's leftover
// sandbox before creating its own.
func TestMainSandbox_SurvivesCrash_ThenGetsReaped(t *testing.T) {
	tmpDir := t.TempDir()

	// Phase 1: a subprocess whose only test panics. The process must crash
	// (non-zero exit, panic trace on stdout/stderr) and its sandbox dir must
	// still be sitting under tmpDir afterward, because a panic unwinds via
	// tRunner's re-panic and no defer in SandboxedMain ever runs.
	out, err := runOperationsSubprocess(t, tmpDir, "^TestPanicGuard$", map[string]string{
		"CTXLOOM_OPTEST_FORCE_PANIC": "1",
	})
	require.Error(t, err, "the panicking subprocess must exit non-zero:\n%s", out)
	assert.True(t, strings.Contains(string(out), "panic:"), "expected a panic trace, got:\n%s", out)

	leftover := sandboxLeftovers(t, tmpDir)
	require.Len(t, leftover, 1,
		"the crashed subprocess's own sandbox dir (holding both home and cwd) should still be present (its defers never ran); got %v", leftover)

	// Phase 2: a second, unrelated subprocess (its single test just Skips)
	// starts under the SAME tmpDir. Its startup reaper must recognize that
	// phase 1's pid is dead and remove that leftover before — or instead of —
	// leaving it behind indefinitely.
	out, err = runOperationsSubprocess(t, tmpDir, "^TestPanicGuard$", nil)
	require.NoError(t, err, "the non-panicking subprocess must exit clean:\n%s", out)

	remaining := sandboxLeftovers(t, tmpDir)
	assert.Empty(t, remaining, "the dead process's sandbox dir must be reaped by the next process to start, got %v", remaining)
}
