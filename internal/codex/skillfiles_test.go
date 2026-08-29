package codex

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestWriteCodexSkillPackages_CleanupPreservesForeignSkill proves
// re-materializing with fewer skills reverts only the manifest-tracked set —
// a foreign, user-authored skill directory survives. Ported off the deleted
// global WriteSkillFiles wrapper (that wrapper, skillsDirFor, and
// codexSkillsDir were test-only dead code) onto writeCodexSkillPackages
// directly, the still-live shared mechanics both the deleted global writer
// and the cell-scoped commands surface (surfaces.go) used — surfaces_test.go
// covers exec-bit preservation and the disabled-skill-not-written case for
// the cell-scoped path; this is the one scenario (a foreign file surviving a
// manifest-scoped cleanup) neither surfaces_test.go nor any other test in
// this package already exercises.
func TestWriteCodexSkillPackages_CleanupPreservesForeignSkill(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/codexhome/skills"
	foreign := dir + "/my-own-skill/SKILL.md"
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("managed"), Mode: 0644}},
	}}
	require.NoError(t, writeCodexSkillPackages(fs, dir, skills))
	exists, _ := afero.Exists(fs, dir+"/humanize/SKILL.md")
	require.True(t, exists)

	// Cleanup: re-materialize with no skills.
	require.NoError(t, writeCodexSkillPackages(fs, dir, nil))

	exists, _ = afero.Exists(fs, dir+"/humanize/SKILL.md")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, foreign)
	assert.True(t, exists, "a foreign skill file must survive cleanup")
	content, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content))
}
