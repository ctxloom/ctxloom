package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// absentOutputDir binds harp to an output dir that does NOT exist on disk, so
// any read or write reaching the OS filesystem there finds nothing.
func absentOutputDir(t *testing.T, harp string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "absent-from-disk")
	sidecar, err := paths.HarpSidecarPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	testsupport.WriteFile(t, afero.NewOsFs(), sidecar, []byte("project_dir: /proj\noutput_dir: "+out+"\n"), 0o600)
	return out
}

// The memory package's file I/O goes through the fs it is handed. Every file
// lives ONLY in a MemMapFs, so an answer reached through the OS filesystem
// would find nothing, and a write through it would land on disk — which each
// case also refuses.
func TestMemory_ReadsAndWritesThroughTheGivenFs(t *testing.T) {
	t.Run("next step", func(t *testing.T) {
		testsupport.Isolate(t)
		out := absentOutputDir(t, testHarp)
		fsys := afero.NewMemMapFs()

		require.NoError(t, WriteNextStep(fsys, testHarp, "merge the branch"))
		got, ok := ReadNextStep(fsys, testHarp)
		require.True(t, ok)
		require.Equal(t, "merge the branch", got)
		_, err := os.Stat(out)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("plan stamp", func(t *testing.T) {
		fsys := afero.NewMemMapFs()
		path := filepath.Join(t.TempDir(), "absent-from-disk", "x.plan.md")
		require.NoError(t, fsys.MkdirAll(filepath.Dir(path), 0o755))
		testsupport.WriteFile(t, fsys, path, []byte("body\n"), 0o600)

		require.NoError(t, StampPlanFile(fsys, path, testHarp))
		got, err := afero.ReadFile(fsys, path)
		require.NoError(t, err)
		require.Contains(t, string(got), testHarp)
	})

	t.Run("distilled sessions", func(t *testing.T) {
		fsys := afero.NewMemMapFs()
		dir := filepath.Join(t.TempDir(), "absent-from-disk")
		require.NoError(t, fsys.MkdirAll(dir, 0o755))
		testsupport.WriteFileString(t, fsys, filepath.Join(dir, "s1.md"), "---\nsession_id: s1\n---\n\nbody\n", 0o600)

		ds, err := LoadDistilledSession(fsys, dir, "s1")
		require.NoError(t, err)
		require.Equal(t, "s1", ds.SessionID)
	})

	t.Run("compactor essence", func(t *testing.T) {
		testsupport.Isolate(t)
		out := absentOutputDir(t, testHarp)
		fsys := afero.NewMemMapFs()
		c, err := NewCompactor(fsys, CompactionConfig{HarpName: testHarp})
		require.NoError(t, err)
		rotation, err := c.rotationEssencePath(testHarp, "s1")
		require.NoError(t, err)
		require.NoError(t, fsys.MkdirAll(filepath.Dir(rotation), 0o755))

		essence, err := c.saveEssence(testHarp, rotation, []byte("essence"))
		require.NoError(t, err)
		found, ok := c.existingEssence("s1", testHarp)
		require.True(t, ok)
		require.Equal(t, essence, found)
		_, err = os.Stat(out)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}
