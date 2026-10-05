package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRun_RefusedOutsideAProject_WritesNoProjectMarker: a `run` in a directory
// that is not a project is refused (no agent to launch). It must leave the
// directory as it found it: a .ctxloom/project-id written by the refused run
// made the directory read as a half-made project, which every later command —
// and `init` — then treated differently. Both the preview and the real run.
func TestRun_RefusedOutsideAProject_WritesNoProjectMarker(t *testing.T) {
	for _, args := range [][]string{
		{"run", "-n", "hi"},
		{"run", "--one-shot", "hi"},
	} {
		t.Run(args[1], func(t *testing.T) {
			dir := testsupport.ProjectDir(t)
			resetApp()
			t.Cleanup(resetApp)

			res := runCLI(t, args...)
			require.ErrorIs(t, res.err, launch.ErrNoAgent, res.all())
			_, statErr := os.Stat(filepath.Join(dir, paths.AppDirName, paths.ProjectIDFileName))
			assert.True(t, os.IsNotExist(statErr), "a refused launch wrote the project marker")
		})
	}
}
