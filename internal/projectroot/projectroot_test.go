package projectroot

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
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

// TestResolve_RelativeAnchorsToProcessCwdNotInjectedFs pins the deliberate
// split: the injected afero.Fs decides
// whether the resolved root EXISTS, but a relative CTXLOOM_ROOT is anchored to
// the launching process's cwd via filepath.Abs — never to the injected fs's
// own root. The two are different filesystems on purpose. CTXLOOM_ROOT is an
// operator-facing environment variable whose relative form can only sensibly
// mean "relative to where the user launched ctxloom"; an fs-rooted reading
// would make the same value mean different directories in-process and
// out-of-process.
//
// The discriminator: the directory exists on the injected fs under its
// RELATIVE name and does not exist at the cwd-anchored absolute path. An
// fs-rooted resolution therefore accepts it and a cwd-anchored one rejects it,
// so this test can only pass under one of the two readings.
func TestResolve_RelativeAnchorsToProcessCwdNotInjectedFs(t *testing.T) {
	testsupport.ProjectDir(t) // isolate env + chdir to a fresh temp dir

	const rel = "relroot"
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(rel, 0o755))

	// Fixture hostility, from resolve's point of view (§11k): the relative name
	// must be a real directory ON THE INJECTED FS, or an fs-rooted resolution
	// would reject it too and the test would pass for the wrong reason.
	info, err := fs.Stat(rel)
	require.NoError(t, err, "fixture is not hostile: %q is not present on the injected fs", rel)
	require.True(t, info.IsDir(), "fixture is not hostile: %q is not a directory on the injected fs", rel)

	// ...and the cwd-anchored form must NOT exist, or both readings would agree.
	abs, err := filepath.Abs(rel)
	require.NoError(t, err)
	_, err = fs.Stat(abs)
	require.Error(t, err, "fixture is not hostile: %q also exists on the injected fs", abs)

	t.Setenv(EnvVar, rel)
	root, ok, invalid := resolve(fs)
	assert.False(t, ok,
		"a relative override is anchored to the process cwd, so a directory that "+
			"exists only at the injected fs's root does not satisfy it")
	assert.Equal(t, "", root)
	assert.Equal(t, rel, invalid, "the raw offending value is reported unmodified")
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

// TestFromEnv_EachDistinctInvalidRootWarns pins that the suppression was a
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

// pkgSourceDir returns this package's SOURCE directory, resolved from the
// compiled-in path of this very file rather than from the process cwd. A
// source-scanning gate that walks "." finds zero files the moment anything
// moves the cwd -- and then matches zero symbols, reports no debt, and exits
// 0. That is a gate that evaporates rather than fails, so the scan root must
// never depend on where the test binary happens to run.
func pkgSourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller could not resolve this test's own source path")
	return filepath.Dir(file)
}

// TestPackageDocNamesEveryExportedSurface pins the invariant the package doc
// asserts: that a reader of the doc comment can discover
// everything this package does. The package grew from one responsibility
// (CTXLOOM_ROOT-first root resolution) to three -- root resolution, git
// worktree classification, and the task-store redirect -- while the doc still
// described only the first, so two thirds of the package were undiscoverable
// from its own front door.
//
// The guard is deliberately mechanical rather than a prose review: every
// exported function and type must be NAMED in the package doc. Adding a fourth
// responsibility without documenting it therefore fails here, which is the
// only thing that keeps the corrected prose true over time.
func TestPackageDocNamesEveryExportedSurface(t *testing.T) {
	dir := pkgSourceDir(t)

	// Parsed file by file rather than with parser.ParseDir, which is deprecated
	// because it associates files with packages without consulting build tags.
	// This package has none, but the per-file walk is exact either way: it
	// admits only non-test files that actually declare package projectroot.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		require.NoError(t, err)
		if f.Name.Name == "projectroot" {
			files = append(files, f)
		}
	}

	// The scan is only meaningful if it actually read this package's files
	// (§11m): an empty or near-empty parse would satisfy every assertion below
	// vacuously.
	require.GreaterOrEqual(t, len(files), 3,
		"scan root %s yielded %d non-test files; expected the whole package", dir, len(files))

	var packageDoc string
	var exported []string
	for _, f := range files {
		if f.Doc != nil && strings.TrimSpace(f.Doc.Text()) != "" {
			packageDoc = f.Doc.Text()
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					exported = append(exported, d.Name.Name)
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					if ts, isType := spec.(*ast.TypeSpec); isType && ts.Name.IsExported() {
						exported = append(exported, ts.Name.Name)
					}
				}
			}
		}
	}

	require.NotEmpty(t, packageDoc, "package projectroot has no package doc comment at all")
	require.NotEmpty(t, exported, "found no exported functions or types -- the parse did not see this package's declarations")

	for _, name := range exported {
		assert.Contains(t, packageDoc, name,
			"exported surface %q is not named anywhere in the package doc, so a reader of the "+
				"doc cannot discover it; document the responsibility it belongs to", name)
	}
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
