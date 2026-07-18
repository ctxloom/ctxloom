package shellenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFakeShellProbe swaps execCommandContext for a fake that ignores the
// real shell invocation and instead runs a tiny Go-less shim: it execs
// /bin/sh -c 'printf %s "$FAKE_SHELLENV_PATH"' so the test controls exactly
// what "PATH" the probe observes, without depending on the actual dev
// container's login shell or rc files. Restored via t.Cleanup, and the
// process-lifetime cache is reset so each test gets a fresh probe.
func withFakeShellProbe(t *testing.T, fakePath string) {
	t.Helper()
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf %s "$FAKE_SHELLENV_PATH"`)
		cmd.Env = append(os.Environ(), "FAKE_SHELLENV_PATH="+fakePath)
		return cmd
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})
}

func TestResolve_NameWithSeparatorPassesThroughUnchanged(t *testing.T) {
	got, err := Resolve("./relative/claude")
	require.NoError(t, err)
	assert.Equal(t, "./relative/claude", got)

	got, err = Resolve("/abs/path/claude")
	require.NoError(t, err)
	assert.Equal(t, "/abs/path/claude", got)
}

func TestResolve_PlainLookPathSucceedsWithoutShellProbe(t *testing.T) {
	// A never-called fake: if Resolve reached the shell probe for a name the
	// bare inherited PATH can already resolve, that would be wasted work on
	// the common path — assert it never happens.
	orig := execCommandContext
	execCommandContext = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("shell probe must not run when exec.LookPath already succeeds")
		return nil
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	got, err := Resolve("sh") // /bin/sh is present in any Unix CI PATH
	require.NoError(t, err)
	assert.NotEmpty(t, got)
}

func TestResolve_FallsBackToLoginShellPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "totally-fake-engine-binary")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	withFakeShellProbe(t, dir)

	got, err := Resolve("totally-fake-engine-binary")
	require.NoError(t, err)
	assert.Equal(t, fakeBin, got)
}

func TestResolve_UnresolvableNameReturnsOriginalLookPathError(t *testing.T) {
	withFakeShellProbe(t, t.TempDir()) // empty dir: shell PATH resolves nothing either

	_, err := Resolve("definitely-does-not-exist-anywhere-xyz")
	require.Error(t, err)
	// exec.LookPath's own error shape names the binary — the resolver must
	// not manufacture a different error than what actually happened.
	assert.Contains(t, err.Error(), "definitely-does-not-exist-anywhere-xyz")
}

func TestResolve_LoginShellPathIsCachedAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "cached-fake-binary")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	calls := 0
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		calls++
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf %s "$FAKE_SHELLENV_PATH"`)
		cmd.Env = append(os.Environ(), "FAKE_SHELLENV_PATH="+dir)
		return cmd
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	_, err := Resolve("cached-fake-binary")
	require.NoError(t, err)
	_, err = Resolve("cached-fake-binary")
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "the login shell probe must run at most once per process")
}

func TestResolve_EmptySHELLFallsBackToBinBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	t.Setenv("SHELL", "")
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "shellless-fake-binary")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	var sawShell string
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		sawShell = name
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf %s "$FAKE_SHELLENV_PATH"`)
		cmd.Env = append(os.Environ(), "FAKE_SHELLENV_PATH="+dir)
		return cmd
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	got, err := Resolve("shellless-fake-binary")
	require.NoError(t, err)
	assert.Equal(t, fakeBin, got)
	assert.Equal(t, "/bin/bash", sawShell, "an empty $SHELL must fall back to /bin/bash")
}
