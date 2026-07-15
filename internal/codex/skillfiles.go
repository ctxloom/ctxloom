package codex

import (
	"path"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// codexSkillManifest tracks which skill files ctxloom wrote, distinct from
// codexManifest (commands' manifest) so the two surfaces' cleanup never
// collides — mirrors claude's .ctxloom-skills-manifest split.
const codexSkillManifest = ".ctxloom-skills-manifest"

// codexSkillsDir resolves Codex's Agent Skills directory: $CODEX_HOME/skills
// (default ~/.codex/skills). Like codexPromptsDir, this is GLOBAL, not
// project-scoped — codex discovers skills only from the top-level
// $CODEX_HOME, mirroring its prompts resolution.
func codexSkillsDir() string {
	home, err := codexHome()
	if err != nil {
		return filepath.Join(".codex", "skills") // last-resort relative
	}
	return filepath.Join(home, "skills")
}

// WriteSkillFiles generates Codex Agent Skill packages from exported skills.
// Files are written to the GLOBAL $CODEX_HOME/skills/<name>/SKILL.md (+
// sibling files), since Codex scans only its own top-level skills dir.
// workDir is unused (codex skills are global, not project-scoped — see
// codexSkillsDir), mirroring WriteCommandFiles. ctxloom tracks its writes via
// a manifest distinct from commands' so the two surfaces' cleanup never
// collides. Only exports with Enabled == true are written. The cell-scoped
// commands surface (surfaces.go) targets cellScopedSkillsDir directly instead
// of calling this function, exactly as its commands counterpart does.
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	return writeCodexSkillPackages(fs, codexSkillsDir(), skills)
}

// writeCodexSkillPackages is the shared manifest-scoped skill-package write,
// parameterized on the target skills dir so both the GLOBAL writer
// (WriteSkillFiles, above) and the CELL-SCOPED commands surface (surfaces.go)
// share one render mapping and can never drift.
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
