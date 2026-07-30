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

// fakeFencedShellCmd builds the fake shim every test below uses in place of
// the real shell invocation: it execs /bin/sh -c 'printf ...' emitting
// banner (optional junk text a real rc file might print), then the fenced
// PATH the real probeLoginShellPath now expects (U117-F01), so these tests
// exercise the same extractFencedPath contract production code does rather
// than a shape only the fake ever produced.
func fakeFencedShellCmd(ctx context.Context, banner, fakePath string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		`[ -n "$FAKE_SHELLENV_BANNER" ] && printf '%s\n' "$FAKE_SHELLENV_BANNER"; `+
			`printf '%s\n%s\n%s\n' "$FAKE_SHELLENV_BEGIN" "$FAKE_SHELLENV_PATH" "$FAKE_SHELLENV_END"`)
	cmd.Env = append(os.Environ(),
		"FAKE_SHELLENV_BANNER="+banner,
		"FAKE_SHELLENV_BEGIN="+pathSentinelBegin,
		"FAKE_SHELLENV_PATH="+fakePath,
		"FAKE_SHELLENV_END="+pathSentinelEnd,
	)
	return cmd
}

// withFakeShellProbe swaps execCommandContext for a fake that ignores the
// real shell invocation and instead runs fakeFencedShellCmd, so the test
// controls exactly what "PATH" the probe observes, without depending on the
// actual dev container's login shell or rc files. Restored via t.Cleanup,
// and the process-lifetime cache is reset so each test gets a fresh probe.
func withFakeShellProbe(t *testing.T, fakePath string) {
	t.Helper()
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return fakeFencedShellCmd(ctx, "", fakePath)
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

// TestResolve_RcFileBannerDoesNotPolluteResolvedPath pins U117-F01: an
// interactive login shell SOURCES the user's real rc files, and a startup
// banner/update-nag/fastfetch-style splash printed to stdout on every
// interactive start is common. Before fencing, that banner text became PATH
// entries verbatim (a bare, un-delimited `echo $PATH` capture). This proves
// the fenced probe still resolves correctly with a banner ahead of it.
func TestResolve_RcFileBannerDoesNotPolluteResolvedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "banner-fake-engine-binary")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return fakeFencedShellCmd(ctx, "Welcome! Your shell has 3 updates available.\nfetch: cpu=fake, mem=fake", dir)
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	got, err := Resolve("banner-fake-engine-binary")
	require.NoError(t, err, "a banner ahead of the fenced PATH must not break resolution")
	assert.Equal(t, fakeBin, got)
}

// TestExtractFencedPath_MissingMarkersFailsLoud is the unit-level pin for the
// parsing primitive itself: unfenced (or truncated/redirected) shell output
// must error, never silently return the raw banner text as if it were PATH.
func TestExtractFencedPath_MissingMarkersFailsLoud(t *testing.T) {
	_, err := extractFencedPath("/usr/bin:/bin\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin marker not found")

	_, err = extractFencedPath(pathSentinelBegin + "\n/usr/bin:/bin\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end marker not found")
}

// TestExtractFencedPath_HappyPath pins the parsing primitive's success shape
// directly, independent of any shell invocation.
func TestExtractFencedPath_HappyPath(t *testing.T) {
	raw := "some banner noise\n" + pathSentinelBegin + "\n/usr/local/bin:/usr/bin:/bin\n" + pathSentinelEnd + "\nmore noise\n"
	got, err := extractFencedPath(raw)
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/bin:/usr/bin:/bin", got)
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
		return fakeFencedShellCmd(ctx, "", dir)
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
		return fakeFencedShellCmd(ctx, "", dir)
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

// loginShellArgs classifies the shell by the basename of $SHELL, and the
// classification is load-bearing: fish and tcsh reject -l in this position, so
// misclassifying one of them makes the probe FAIL and the whole PATH recovery
// this package exists for silently does nothing. $SHELL is user-supplied
// environment, so the classification must survive the shapes it really arrives
// in — a case-insensitive filesystem's capitalization, and stray surrounding
// whitespace from a mis-set export.
func TestLoginShellArgs_ClassifiesRobustly(t *testing.T) {
	const cmd = "echo x"
	interactiveOnly := []string{"-i", "-c", cmd}
	loginInteractive := []string{"-l", "-i", "-c", cmd}

	cases := []struct {
		shell string
		want  []string
	}{
		{"/usr/bin/fish", interactiveOnly},
		{"/bin/tcsh", interactiveOnly},
		{"/bin/bash", loginInteractive},
		{"/bin/zsh", loginInteractive},
		{"/bin/sh", loginInteractive},

		// A case-insensitive filesystem (APFS by default) resolves this to the
		// same binary, so it must classify the same way.
		{"/usr/local/bin/Fish", interactiveOnly},
		{"/bin/TCSH", interactiveOnly},

		// Whitespace around the value: `export SHELL="/usr/bin/fish "`.
		{" /usr/bin/fish ", interactiveOnly},
		{"/bin/bash\n", loginInteractive},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			assert.Equal(t, tc.want, loginShellArgs(tc.shell, cmd))
		})
	}
}
