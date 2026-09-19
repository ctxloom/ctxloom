package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestFindAppDirCtxloomRoot covers the CTXLOOM_ROOT override branch of
// findAppDir: a valid root is authoritative and its .ctxloom is materialized,
// while an invalid root is ignored and creates nothing under the bad path.
func TestFindAppDirCtxloomRoot(t *testing.T) {
	t.Run("creates_ctxloom_under_valid_root", func(t *testing.T) {
		testsupport.Isolate(t)
		root := "/work/proj"
		t.Setenv(projectroot.EnvVar, root)

		fs := afero.NewMemMapFs()
		// The root is validated through the same fs findAppDir writes to, so
		// it must exist there — no real disk involved.
		if err := fs.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("seed root: %v", err)
		}
		path, src := findAppDir(fs)

		want := filepath.Join(root, AppDirName)
		assert.Equal(t, want, path)
		assert.Equal(t, SourceProject, src,
			"a named root resolves as a project dir, not the home fallback")

		exists, _ := afero.DirExists(fs, want)
		assert.True(t, exists,
			"findAppDir must create $CTXLOOM_ROOT/.ctxloom when absent")
	})

	t.Run("invalid_root_falls_through_and_creates_nothing", func(t *testing.T) {
		testsupport.Isolate(t) // HOME rooted at a temp dir for the fallback
		bad := "/work/missing" // never created on fs
		t.Setenv(projectroot.EnvVar, bad)

		fs := afero.NewMemMapFs()
		path, _ := findAppDir(fs)

		badApp := filepath.Join(bad, AppDirName)
		assert.NotEqual(t, badApp, path,
			"an invalid override must not be treated as the project root")
		exists, _ := afero.DirExists(fs, badApp)
		assert.False(t, exists,
			"nothing should be created beneath an invalid CTXLOOM_ROOT")
	})
}

// TestFindAppDirBareTempDirDoesNotEscapeToSharedTempRoot is the red test for
// the cross-test state leak: findAppDir's walk-up-from-cwd loop had no
// boundary at all short of the filesystem root, so a bare t.TempDir() (no
// project .ctxloom of its own) walks straight past the temp root and can
// resolve to whatever `.ctxloom` happens to already live directly in the
// shared OS temp directory (a real, long-lived one on this host) — silently
// sharing state with any other process or test that landed on the same
// fallback. This test uses the REAL OS filesystem (not afero.MemMapFs)
// because the defect only manifests via real disk contents at a real shared
// path; it must not create or depend on a .ctxloom actually existing at
// os.TempDir() (if the host happens to have one, the test host's own
// isolation would be broken already) -- it asserts the *shape* of the
// boundary instead: whatever findAppDir resolves to must never be the OS
// temp dir's own .ctxloom, and must be contained either under the fresh
// project dir or under the fresh (isolated) HOME temp dir.
func TestFindAppDirBareTempDirDoesNotEscapeToSharedTempRoot(t *testing.T) {
	home := testsupport.Isolate(t) // HOME -> its own fresh temp dir
	dir := t.TempDir()             // project cwd: a SIBLING fresh temp dir, no .ctxloom of its own
	testsupport.ChangeDir(t, dir)

	tmpRoot := os.TempDir()
	sharedMarker := filepath.Join(tmpRoot, AppDirName)

	fs := afero.NewOsFs()
	path, _ := findAppDir(fs)

	assert.NotEqual(t, sharedMarker, path,
		"findAppDir must never resolve to the shared OS temp root's own .ctxloom -- "+
			"that directory is multi-tenant scratch space, not a project, and returning it "+
			"silently shares state with every other test/process that hit the same fallback")

	inProjectDir := strings.HasPrefix(path, dir+string(filepath.Separator)) || path == dir
	inHomeDir := strings.HasPrefix(path, home+string(filepath.Separator)) || path == home
	assert.True(t, inProjectDir || inHomeDir,
		"findAppDir resolved to %q, which is neither under the fresh project dir %q "+
			"nor the fresh isolated HOME %q -- it escaped the test's sandbox", path, dir, home)
}
