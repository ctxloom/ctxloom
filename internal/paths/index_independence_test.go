// Package paths_test holds the assertions that must see BOTH `paths` packages
// at once. internal/shared/tasks/paths imports internal/paths, so an in-package
// test could not name it; an external test package can.
package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	taskpaths "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestIndexFileNames_AreIndependentDecisions records why the two IndexFileName
// constants are NOT a duplication to collapse, so that the next reader who
// notices two `paths` packages each declaring `IndexFileName = "index.yaml"`
// does not merge them.
//
// AppDirName genuinely was connascence of value: one directory, spelled twice,
// where a drift in either spelling breaks the other package silently. It is now
// an alias. IndexFileName is the opposite case. These are two DIFFERENT FILES
// in two different directories, holding unrelated data:
//
//	~/.ctxloom/sessions/index.yaml   binds harp names to backend sessions
//	~/.ctxloom/projects/index.yaml   binds stable project-ids to current paths
//
// They coincide only in having independently been given the conventional name
// for an index. Aliasing one onto the other would COUPLE two decisions that are
// free to differ: renaming the session index would silently rename the project
// registry, which no reader of either rename would expect. Sharing a spelling
// is not sharing a meaning.
func TestIndexFileNames_AreIndependentDecisions(t *testing.T) {
	testsupport.Isolate(t)

	session, err := paths.SessionIndexPath()
	require.NoError(t, err)
	registry, err := taskpaths.ProjectRegistryPath()
	require.NoError(t, err)

	assert.NotEqual(t, session, registry,
		"the session index and the project registry are distinct files")
	assert.NotEqual(t, filepath.Dir(session), filepath.Dir(registry),
		"they live in different directories, which is what makes the shared leaf name harmless")
	assert.Equal(t, paths.SessionsDir, filepath.Base(filepath.Dir(session)))
	assert.Equal(t, "projects", filepath.Base(filepath.Dir(registry)))

	// The two leaf names are asserted SEPARATELY and literally. That is the
	// point: if either constant is ever aliased onto the other, renaming the
	// survivor drags the alias with it and exactly one of these two lines
	// fails — which is the coupling this test exists to forbid, made visible.
	assert.Equal(t, "index.yaml", filepath.Base(session),
		"the session index's name is internal/paths' decision alone")
	assert.Equal(t, "index.yaml", filepath.Base(registry),
		"the project registry's name is internal/shared/tasks/paths' decision alone")
}

// TestAppDirName_IsASingleSpelling is the other half of the row, and the half
// that WAS a real duplication. The task store deliberately shares ctxloom's
// dot-directory rather than minting a parallel one, so two independent
// ".ctxloom" literals could drift and stop the task store's documented opt-out
// from matching the directory `ctxloom init` creates — with no error at either
// end. The shared package now aliases rather than re-declaring; this fails if
// anyone re-introduces a second literal that drifts.
func TestAppDirName_IsASingleSpelling(t *testing.T) {
	assert.Equal(t, paths.AppDirName, taskpaths.AppDirName,
		"the task store shares ctxloom's app directory; two spellings that drift break it silently")
}
