package codex

import (
	"path"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// codexSkillManifest tracks which skill files ctxloom wrote, distinct from
// codexManifest (commands' manifest) so the two surfaces' cleanup never
// collides — mirrors claude's .ctxloom-skills-manifest split.
const codexSkillManifest = ".ctxloom-skills-manifest"

// writeCodexSkillPackages is the shared manifest-scoped skill-package write,
// parameterized on the target skills dir. Used by the CELL-SCOPED commands
// surface (surfaces.go); the GLOBAL WriteSkillFiles writer that used to share
// this render mapping was deleted as dead (U045-F07 — test-only, dragging
// skillsDirFor/codexSkillsDir with it).
func writeCodexSkillPackages(fs afero.Fs, skillsDir string, skills []agent.SkillExport) error {
	return agent.WriteManagedPackageFiles(fs, skillsDir, codexSkillManifest, skills,
		func(s agent.SkillExport) bool { return s.Enabled },
		func(s agent.SkillExport) string { return s.Name },
		func(s agent.SkillExport) ([]agent.PackageFile, error) {
			out := make([]agent.PackageFile, len(s.Files))
			for i, f := range s.Files {
				out[i] = agent.PackageFile{
					RelPath: path.Join(s.Name, f.RelPath),
					Content: f.Content,
					Mode:    f.Mode,
				}
			}
			return out, nil
		},
	)
}
