package config

import (
	"path/filepath"
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
