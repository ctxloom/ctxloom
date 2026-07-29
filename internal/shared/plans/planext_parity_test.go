package plans

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// U115-F06: this package used to re-declare the plan-file vocabulary
// internal/paths already owns — an unexported `planExt = ".plan.md"` next to
// paths.PlanFileExt, with nothing linking the two. Recognition now comes from
// the shared constant; this pins that a file named by paths.PlanFileExt is the
// thing List finds and Show accepts, so a change to the constant moves both.
func TestPlanRecognition_UsesPathsPlanFileExt(t *testing.T) {
	root := t.TempDir()
	harpDir := filepath.Join(root, "vital-deaf-stunt")
	require.NoError(t, os.MkdirAll(harpDir, 0o755))

	planPath := filepath.Join(harpDir, "release"+paths.PlanFileExt)
	require.NoError(t, os.WriteFile(planPath, []byte("---\ntitle: Release\n---\nbody\n"), 0o644))
	// A neighbouring non-plan file must not be picked up.
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "notes.md"), []byte("x"), 0o644))

	got, err := List(root)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "release", got[0].Name, "the extension must be trimmed by paths.PlanFileExt")
	assert.Equal(t, "Release", got[0].Title)

	// Show's gate is the same constant.
	_, err = Show(filepath.Join(harpDir, "release.md"))
	assert.Error(t, err, "a non-plan extension must be refused")
}
