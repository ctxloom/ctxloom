package operations

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
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
	dir, err := sourcedir.Dir()
	require.NoError(t, err, "must resolve this package's source directory")
	return dir
}

// freshSandboxEnv is the environment for a child that must mint its OWN
// sandbox under tmpDir (TMPDIR pinned there, so the child's sandbox — and
// this test's assertions about it — are confined to a throwaway root instead
// of the developer's real /tmp).
//
// The parent's own sandbox marker is STRIPPED: SandboxedMain treats an
// inherited testsupport.SandboxRootEnv as "already inside a sandbox" and
// adopts it instead of minting its own — the right call for a re-exec'd
// child, and exactly wrong here, where the child's own sandbox is the thing
// under observation. The parent's own TMPDIR is stripped for the same reason
// the pin exists: exec.Cmd keeps the LAST value of a duplicated key, so an
// inherited TMPDIR appended after the pin would silently win and land the
// child's sandbox where these assertions never look. HOME is left as the
// parent's sandbox home on purpose: SandboxedMain pins the go toolchain's
// cache directories explicitly, so a child's `go test` build phase does not
// need the real HOME to find them.
func freshSandboxEnv(tmpDir string, extraEnv map[string]string) []string {
	env := []string{"TMPDIR=" + tmpDir}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, testsupport.SandboxRootEnv+"=") || strings.HasPrefix(kv, "TMPDIR=") {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// runOperationsSubprocess runs `go test -run <pattern>` on this package in a
// fresh process whose sandbox lands under tmpDir (see freshSandboxEnv).
func runOperationsSubprocess(t *testing.T, tmpDir, pattern string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "test", "-run", pattern, "-count=1", ".")
	cmd.Dir = pkgDir(t)
	cmd.Env = freshSandboxEnv(tmpDir, extraEnv)
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

// blockGuardReadyEnv names the file TestBlockGuard touches once it is inside
// m.Run() and about to block; its presence is what makes the parent's signal
// land on a process that has finished installing its sandbox and handler,
// rather than racing the startup window.
const blockGuardReadyEnv = "CTXLOOM_OPTEST_BLOCK_READY"

// TestBlockGuard exists only to be run as a subprocess by
// TestMainSandbox_SignalCleansUpBeforeExit: it announces readiness, then
// blocks so the parent can deliver a signal to a process that is mid-run.
// The sleep is bounded so a handler that never fires ends in a normal exit
// the parent can distinguish from a signalled one.
func TestBlockGuard(t *testing.T) {
	ready := os.Getenv(blockGuardReadyEnv)
	if ready == "" {
		t.Skip("subprocess-only guard test; set " + blockGuardReadyEnv + " to invoke")
	}
	require.NoError(t, os.WriteFile(ready, nil, 0o600))
	time.Sleep(time.Minute)
}

// TestMainSandbox_SignalCleansUpBeforeExit proves the half of the crash story
// a process CAN handle itself: SIGTERM, unlike a panic, is catchable, so a
// signalled process removes its own sandbox before dying instead of leaving
// it for the next process's reaper. The child is THIS test binary re-exec'd
// directly (not through `go test`, whose own signal forwarding would be the
// thing under test), so the signal lands on SandboxedMain's handler.
func TestMainSandbox_SignalCleansUpBeforeExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery to a child is not supported on windows")
	}
	tmpDir := t.TempDir()
	ready := filepath.Join(tmpDir, "ready")

	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestBlockGuard$", "-test.timeout=2m")
	cmd.Dir = pkgDir(t)
	cmd.Env = freshSandboxEnv(tmpDir, map[string]string{blockGuardReadyEnv: ready})
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("the child never announced readiness:\n%s", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Len(t, sandboxLeftovers(t, tmpDir), 1,
		"the child must be running inside its own sandbox before it is signalled")

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "the signalled child must exit, not vanish; err=%v output:\n%s", err, out.String())
	assert.Equal(t, 128+int(syscall.SIGTERM), exitErr.ExitCode(),
		"the handler exits with the conventional 128+signal code (a default-action death reports -1); output:\n%s", out.String())
	assert.Empty(t, sandboxLeftovers(t, tmpDir),
		"a signalled process must remove its own sandbox before dying; output:\n%s", out.String())
}
