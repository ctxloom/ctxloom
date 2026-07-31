package projectroot

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestResolve(t *testing.T) {
	testsupport.Isolate(t) // clears CTXLOOM_ROOT (and the rest) so subtests start clean

	t.Run("unset", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		root, ok, invalid := resolve(afero.NewMemMapFs())
		assert.False(t, ok)
		assert.Equal(t, "", root)
		assert.Equal(t, "", invalid, "an unset var is not invalid — no warning should fire")
	})

	t.Run("valid_dir", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		dir := "/work/proj"
		require.NoError(t, fs.MkdirAll(dir, 0o755))
		t.Setenv(EnvVar, dir)
		root, ok, invalid := resolve(fs)
		assert.True(t, ok)
		assert.Equal(t, "", invalid)
		assert.Equal(t, dir, root)
	})

	t.Run("nonexistent_path", func(t *testing.T) {
		bad := "/work/does-not-exist"
		t.Setenv(EnvVar, bad)
		root, ok, invalid := resolve(afero.NewMemMapFs())
		assert.False(t, ok)
		assert.Equal(t, "", root)
		assert.Equal(t, bad, invalid, "the raw offending value is reported for the warning")
	})

	t.Run("file_not_dir", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		f := "/work/afile"
		require.NoError(t, afero.WriteFile(fs, f, []byte("x"), 0o644))
		t.Setenv(EnvVar, f)
		root, ok, invalid := resolve(fs)
		assert.False(t, ok, "a regular file is not a valid root")
		assert.Equal(t, "", root)
		assert.Equal(t, f, invalid)
	})

	t.Run("relative_is_anchored_to_cwd", func(t *testing.T) {
		dir := testsupport.ProjectDir(t) // isolate env + chdir to a fresh temp dir
		abs, err := filepath.Abs(dir)
		require.NoError(t, err)
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(abs, 0o755))
		t.Setenv(EnvVar, ".")
		root, ok, _ := resolve(fs)
		require.True(t, ok)
		assert.Equal(t, filepath.Clean(abs), root,
			"a relative override resolves against the launching cwd")
	})
}

func TestFromEnv(t *testing.T) {
	testsupport.Isolate(t)

	t.Run("valid", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		dir := "/work/proj"
		require.NoError(t, fs.MkdirAll(dir, 0o755))
		t.Setenv(EnvVar, dir)
		got, ok := FromEnv(fs)
		assert.True(t, ok)
		assert.Equal(t, dir, got)
	})

	t.Run("unset", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		got, ok := FromEnv(afero.NewMemMapFs())
		assert.False(t, ok)
		assert.Equal(t, "", got)
	})

	t.Run("invalid_returns_unset_shape", func(t *testing.T) {
		t.Setenv(EnvVar, "/work/nope")
		got, ok := FromEnv(afero.NewMemMapFs())
		assert.False(t, ok, "invalid override behaves exactly like unset to callers")
		assert.Equal(t, "", got)
	})
}

// TestFromEnv_EachDistinctInvalidRootWarns pins U092-F08. The suppression was a
// package-level sync.Once keyed on NOTHING, so the first invalid CTXLOOM_ROOT a
// process ever saw permanently silenced every later one — including a
// completely different offending value. The suppression that is actually wanted
// is per-message (identical value, one line), which is exactly clidiag.WarnOnce.
//
// The two values are derived from t.TempDir() so their formatted lines are
// unique to this run: clidiag's dedup map is process-global, so a hard-coded
// path could be pre-seeded by another test and leave this pin green for a
// reason unrelated to the defect.
func TestFromEnv_EachDistinctInvalidRootWarns(t *testing.T) {
	testsupport.Isolate(t)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	base := t.TempDir()
	first := filepath.Join(base, "no-such-root-a")
	second := filepath.Join(base, "no-such-root-b")

	// The fixture is only hostile if BOTH values really are invalid roots; a
	// path that happened to exist would warn zero times and prove nothing.
	for _, bad := range []string{first, second} {
		_, err := os.Stat(bad)
		require.True(t, os.IsNotExist(err), "fixture is not hostile: %s exists", bad)
	}

	t.Setenv(EnvVar, first)
	_, ok := FromEnv(afero.NewOsFs())
	require.False(t, ok)
	require.Contains(t, sink.String(), first,
		"fixture is not hostile: the first invalid root produced no warning at all")

	t.Setenv(EnvVar, second)
	_, ok = FromEnv(afero.NewOsFs())
	require.False(t, ok)
	assert.Contains(t, sink.String(), second,
		"a second, DIFFERENT invalid CTXLOOM_ROOT must still be reported — the "+
			"suppression exists to collapse repeats of one message, not to mute the variable")
}

// TestFromEnv_RepeatedInvalidRootWarnsOnce pins the half of the suppression that
// must survive the fix: the same offending value, resolved many times in one
// process (config.Load runs on every command), stays a single line.
func TestFromEnv_RepeatedInvalidRootWarnsOnce(t *testing.T) {
	testsupport.Isolate(t)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	bad := filepath.Join(t.TempDir(), "no-such-root-repeat")
	t.Setenv(EnvVar, bad)
	for range 5 {
		_, ok := FromEnv(afero.NewOsFs())
		require.False(t, ok)
	}
	assert.Equal(t, 1, strings.Count(sink.String(), bad),
		"one offending value warns once however often it is resolved")
}

func TestWorkDir(t *testing.T) {
	// WorkDir is the process-level resolver: it operates on the OS filesystem
	// and on real git-root / cwd detection, so these exercise real dirs.
	t.Run("env_beats_git", func(t *testing.T) {
		testsupport.Isolate(t)
		dir := t.TempDir()
		t.Setenv(EnvVar, dir)
		want, _ := filepath.Abs(dir)
		assert.Equal(t, filepath.Clean(want), WorkDir(),
			"a valid CTXLOOM_ROOT must win over git-root detection")
	})

	t.Run("invalid_env_falls_through_to_git", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv(EnvVar, filepath.Join(t.TempDir(), "nope"))
		// The test runs inside the ctxloom repo, so git-root detection succeeds.
		expected, err := gitutil.FindRoot(".")
		require.NoError(t, err)
		assert.Equal(t, expected, WorkDir(),
			"an invalid override is ignored and git-root detection applies")
	})

	t.Run("falls_through_to_cwd_outside_repo", func(t *testing.T) {
		testsupport.ProjectDir(t) // isolate env + chdir to a fresh non-git temp dir
		cwd, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, cwd, WorkDir(),
			"with no override and no git root, WorkDir resolves to the cwd")
	})
}

func TestRootFromFallback(t *testing.T) {
	t.Run("true_outside_repo_without_override", func(t *testing.T) {
		testsupport.ProjectDir(t) // isolate env + chdir to a fresh non-git temp dir
		assert.True(t, RootFromFallback(),
			"no override and no git root means the cwd fallback — warn-worthy")
	})

	t.Run("false_with_valid_override", func(t *testing.T) {
		testsupport.ProjectDir(t) // non-git cwd, so only the override can suppress it
		dir := t.TempDir()
		t.Setenv(EnvVar, dir)
		assert.False(t, RootFromFallback(),
			"a deliberate CTXLOOM_ROOT is not a silent fallback")
	})

	t.Run("false_inside_repo", func(t *testing.T) {
		testsupport.Isolate(t) // clear env; the test runs inside the ctxloom repo
		require.NoError(t, func() error { _, err := gitutil.FindRoot("."); return err }(),
			"precondition: tests run inside a git repo")
		assert.False(t, RootFromFallback(),
			"a discovered git root is a stable project root, not a fallback")
	})
}
