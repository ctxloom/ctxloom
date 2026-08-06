package codex

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// writeCodexSkillPackages is the shared manifest-scoped skill-package write,
// parameterized on the target skills dir. Used by the CELL-SCOPED commands
// surface (surfaces.go); the GLOBAL WriteSkillFiles writer that used to share
// this render mapping was deleted as dead — test-only, dragging
// skillsDirFor/codexSkillsDir with it.
func writeCodexSkillPackages(fs afero.Fs, skillsDir string, skills []agent.SkillExport) error {
	return agent.WriteManagedSkillPackages(fs, skillsDir, skills)
}
