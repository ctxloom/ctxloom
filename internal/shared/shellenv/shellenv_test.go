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

// TestProbeLoginShellPath_SilentShellIsAnError pins U117-F03's subject. The
// row claims probeLoginShellPath returns ("", nil) -- success with an empty
// payload -- when the shell runs but prints nothing, and that the cache then
// blesses that for the process lifetime.
//
// That is what a bare `echo "$PATH"` capture did. Since the probe was fenced,
// a shell that prints nothing produces neither sentinel, so extractFencedPath
// refuses and the probe fails LOUDLY, naming the likely cause. A shell that
// prints its startup noise and no PATH fails the same way.
func TestProbeLoginShellPath_SilentShellIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	cases := map[string]string{
		"prints nothing at all": "",
		"prints only a banner":  "printf 'Welcome to your shell\\n'",
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			orig := execCommandContext
			execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "/bin/sh", "-c", script)
			}
			resetCacheForTest()
			t.Cleanup(func() {
				execCommandContext = orig
				resetCacheForTest()
			})

			got, err := probeLoginShellPath()
			require.Error(t, err, "a probe that recovered no PATH must not report success")
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), "begin marker not found")
		})
	}
}

// TestResolve_EmptyLoginShellPathFallsBackToTheOriginalError pins the second
// half of U117-F03: the one ("", nil) outcome that CAN still occur is a shell
// whose PATH is genuinely empty, and Resolve must treat that as "resolved to
// nothing" rather than searching an empty PATH and inventing a different
// failure. The caller keeps exec.LookPath's own error, naming the binary.
func TestResolve_EmptyLoginShellPathFallsBackToTheOriginalError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	withFakeShellProbe(t, "") // sentinels present, PATH between them empty

	path, err := loginShellPath()
	require.NoError(t, err, "an empty PATH between the sentinels is a resolved-to-nothing outcome, not a failure")
	assert.Empty(t, path)

	_, err = Resolve("definitely-does-not-exist-anywhere-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-does-not-exist-anywhere-xyz")
}

// TestProbeLoginShellPath_FailureCarriesTheShellsStderr pins U117-F02. The
// probe runs the user's REAL login shell against their REAL rc files, so
// when it fails the shell has almost always already said why -- "command not
// found", a syntax error with a file and line, an nvm/rbenv init that
// aborted. Discarding that stream leaves the operator an exit status and no
// way to learn which of their startup files broke a feature whose entire
// purpose is to be invisible when it works.
func TestProbeLoginShellPath_FailureCarriesTheShellsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c",
			`printf 'zshrc:42: command not found: nvm\n' >&2; exit 127`)
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	_, err := probeLoginShellPath()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zshrc:42: command not found: nvm",
		"the shell's own diagnosis is the only thing that names the broken startup file")
	assert.Contains(t, err.Error(), "127", "the exit status must survive alongside it")
}

// TestResolve_SkipsAFileTheCurrentUserCannotExecute pins U117-F05.
// isExecutableFile tested RAW MODE BITS (Mode()&0o111 != 0), which asks "does
// SOMEBODY have an execute bit here?", not "can THIS process exec it?".
// exec.LookPath's Unix implementation asks the second question, via an
// effective-uid access check, so the two disagree on a file whose owner bit
// is clear and whose group/other bits are set (0o011): the mode-bit test
// accepts it, the kernel refuses it.
//
// The consequence is not a cosmetic divergence. lookPathIn returns the FIRST
// accepting directory, so one unexecutable shim early in the login-shell PATH
// shadowed the real binary further down, and the engine launch failed with
// EACCES on a path ctxloom had just chosen — worse than the ENOENT this whole
// package exists to prevent, because it looks like the binary was found.
func TestResolve_SkipsAFileTheCurrentUserCannotExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH resolution is POSIX-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the execute permission check entirely")
	}
	const bin = "shadowed-fake-engine-binary"

	shadowDir := t.TempDir()
	shadow := filepath.Join(shadowDir, bin)
	require.NoError(t, os.WriteFile(shadow, []byte("#!/bin/sh\n"), 0o644))
	// Group and other may execute; the OWNER may not. POSIX consults the owner
	// bits alone when euid matches, so this process cannot exec it despite
	// Mode()&0o111 being non-zero.
	require.NoError(t, os.Chmod(shadow, 0o011))

	realDir := t.TempDir()
	real := filepath.Join(realDir, bin)
	require.NoError(t, os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755))

	withFakeShellProbe(t, shadowDir+string(filepath.ListSeparator)+realDir)

	got, err := Resolve(bin)
	require.NoError(t, err)
	assert.Equal(t, real, got, "an unexecutable file must not shadow the executable one behind it")
}
