package antigravity

import (
	"path"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// antigravitySkillsDir (capabilities.go) is agy's workspace Agent Skills
// directory, .agents/skills — the SAME parent directory the (legacy, flat)
// command writer targets. This is not a collision: WriteCommandFiles writes
// flat `<name>.md` files directly in that dir, while WriteSkillFiles below
// writes `<name>/SKILL.md` DIRECTORIES — a distinct filesystem entry per name,
// so the two surfaces coexist under the shared parent. VERIFIED against agy's
// own bundled documentation (~/.gemini/antigravity-cli/builtin/skills/
// agy-customizations/docs/skills.md, agy 1.1.2, read directly — no model
// tokens spent): "A skill must be structured as a directory within a
// `skills/` folder inside a customization root (e.g., `.agents/skills/`)",
// and agy's own customization guide lists workspace discovery at `.agents/`
// (or `.agent/`, `_agents/`, `_agent/`) walked up from cwd to the repo root.
// This is HIGH CONFIDENCE (agy's own primary docs), unlike the legacy flat
// command writer's unverified discovery (see capabilities.go's doc comment).

// antigravitySkillManifest tracks the skill directories ctxloom wrote,
// distinct from antigravityManifest (commands' flat-file manifest) so the two
// surfaces' cleanup never collides — mirrors claude's
// .ctxloom-skills-manifest split.
const antigravitySkillManifest = ".ctxloom-skills-manifest"

// WriteSkillFiles materializes agy's Agent Skills surface: every enabled
// skill package lands at .agents/skills/<name>/SKILL.md (+ sibling files),
// exec bit preserved on scripts/ entries. ctxloom tracks its writes via a
// manifest distinct from commands' so the two surfaces' cleanup never
// collides (see agent.WriteManagedPackageFiles for the shared mechanics).
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(antigravitySkillsDir))
	return agent.WriteManagedPackageFiles(fs, skillsDir, antigravitySkillManifest, skills,
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
